package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// composeNetAndContainers answers "network inspect app" with two Compose
// containers and one plain container, and "inspect <ids>" with their
// per-network aliases the way Docker Engine 25+ reports them.
func composeNetAndContainers(t *testing.T, containerInspectErr error) *netFakeDocker {
	t.Helper()
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		switch {
		case isNetInspect(args):
			return fmt.Appendf(nil, `[{"Name":"app","Id":%q,"Driver":"bridge","Scope":"local","Internal":true,"Labels":{},
				"Containers":{"c1":{"Name":"app-redis-1"},"c2":{"Name":"app-postgres-1"},"c3":{"Name":"plain"}}}]`, id('a')), nil
		case len(args) >= 2 && args[0] == "inspect":
			if containerInspectErr != nil {
				return nil, containerInspectErr
			}
			return []byte(`[
				{"Id":"c1","Name":"/app-redis-1","NetworkSettings":{"Networks":{
					"app":{"Aliases":["app-redis-1","redis","a863aa94ac7e"]},
					"other":{"Aliases":["cache-only-on-other"]}}}},
				{"Id":"c2","Name":"/app-postgres-1","NetworkSettings":{"Networks":{
					"app":{"Aliases":["app-postgres-1","postgres"]}}}},
				{"Id":"c3","Name":"/plain","NetworkSettings":{"Networks":{"app":{"Aliases":null}}}}
			]`), nil
		}
		return nil, errors.New("unexpected docker call: " + strings.Join(args, " "))
	}}
	f.install(t)
	return f
}

func TestRED_ResolveNetworks_ServiceNamesAreComposeAliases(t *testing.T) {
	composeNetAndContainers(t, nil)
	nets, err := ResolveNetworks(context.Background(), []string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	// Compose service aliases replace the generated container names; a
	// container without an alias keeps its name; short container IDs and
	// aliases that belong to another network are not listed.
	if got := strings.Join(nets[0].ServiceNames, ","); got != "plain,postgres,redis" {
		t.Errorf("ServiceNames = %q, want plain,postgres,redis", got)
	}
}

func TestRED_ResolveNetworks_ServiceNamesFallBackToContainerNames(t *testing.T) {
	composeNetAndContainers(t, errors.New("inspect failed"))
	nets, err := ResolveNetworks(context.Background(), []string{"app"})
	if err != nil {
		t.Fatalf("a failed container inspect must not fail network resolution: %v", err)
	}
	if got := strings.Join(nets[0].ServiceNames, ","); got != "app-postgres-1,app-redis-1,plain" {
		t.Errorf("ServiceNames = %q, want the container names as a fallback", got)
	}
}

// A short service name that happens to be a hex prefix of the container ID
// ("db", "cafe") is still a service name; only short-ID aliases are dropped.
func TestRED_ServiceNames_KeepsHexLookingServiceNames(t *testing.T) {
	cid := "db" + strings.Repeat("0", 62)
	f := &netFakeDocker{handler: func(args []string) ([]byte, error) {
		switch {
		case isNetInspect(args):
			return fmt.Appendf(nil, `[{"Name":"app","Id":%q,"Driver":"bridge","Scope":"local","Internal":true,"Labels":{},
				"Containers":{%q:{"Name":"app-db-1"}}}]`, id('a'), cid), nil
		case len(args) >= 2 && args[0] == "inspect":
			return fmt.Appendf(nil, `[{"Id":%q,"NetworkSettings":{"Networks":{"app":{"Aliases":["app-db-1","db",%q]}}}}]`, cid, cid[:12]), nil
		}
		return nil, errors.New("unexpected docker call")
	}}
	f.install(t)
	nets, err := ResolveNetworks(context.Background(), []string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(nets[0].ServiceNames, ","); got != "db" {
		t.Errorf("ServiceNames = %q, want db", got)
	}
}
