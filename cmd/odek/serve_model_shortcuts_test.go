package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleModelList_DeepSeekShortcuts(t *testing.T) {
	for _, provider := range []string{"deepseek", "", "custom"} {
		for _, online := range []bool{false, true} {
			list := func(context.Context) ([]listedModel, error) {
				if !online {
					return nil, errors.New("offline")
				}
				return []listedModel{{ID: "deepseek-flash"}, {ID: "deepseek-v4-pro"}}, nil
			}
			w := httptest.NewRecorder()
			handleModelList("deepseek-flash", list, provider)(w, httptest.NewRequest(http.MethodGet, "/api/models", nil))
			var models []modelEntry
			if err := json.Unmarshal(w.Body.Bytes(), &models); err != nil {
				t.Fatal(err)
			}
			if len(models) == 0 || models[0].ID != "deepseek-flash" || !models[0].Current {
				t.Fatalf("current model must remain first: %+v", models)
			}
			seen := map[string]bool{}
			for _, model := range models {
				if seen[model.ID] {
					t.Fatalf("duplicate model: %q", model.ID)
				}
				seen[model.ID] = true
				if model.MaxContext != 1_000_000 {
					t.Errorf("%s context = %d", model.ID, model.MaxContext)
				}
			}
			if seen["deepseek-pro"] != (provider != "custom") {
				t.Errorf("provider %q shortcuts: %+v", provider, models)
			}
			if online && !seen["deepseek-v4-pro"] {
				t.Error("legacy model removed from provider catalog")
			}
		}
	}
}
