package telegram

import "testing"

// Media callbacks learn whether the message was forwarded, as the text
// callback does: a forwarded recording or caption is not the sender's own
// request, so the surface must not treat it as the principal's words.
func TestRED_MediaCallbacksReportForwarded(t *testing.T) {
	ts := testServer(t, nil)
	defer ts.Close()
	h := NewHandler(testBot(t, ts))
	h.Config.AllowAllUsers = true

	got := map[string]bool{}
	h.OnVoiceMessage = func(_ int64, _ int, _ string, forwarded bool, _ int64) (string, error) {
		got["voice"] = forwarded
		return "", nil
	}
	h.OnPhotoMessage = func(_ int64, _ int, _ []string, _ string, forwarded bool, _ int64) (string, error) {
		got["photo"] = forwarded
		return "", nil
	}
	h.OnDocumentMessage = func(_ int64, _ int, _ string, _ string, _ string, forwarded bool, _ int64) (string, error) {
		got["document"] = forwarded
		return "", nil
	}
	send := func(id int, forwarded bool) {
		for _, m := range []*Message{
			{Voice: &Voice{FileID: "v"}},
			{Photo: []PhotoSize{{FileID: "p"}}, Caption: "deploy the staging site"},
			{Document: &Document{FileID: "d", FileName: "a.txt"}, Caption: "summarize this"},
		} {
			m.Chat, m.From = &Chat{ID: 31}, &User{ID: 32}
			if forwarded {
				m.ForwardOrigin = &ForwardOrigin{}
			}
			h.HandleUpdate(Update{ID: id, Message: m})
			id++
		}
	}
	send(1, false)
	for _, kind := range []string{"voice", "photo", "document"} {
		if v, ok := got[kind]; !ok || v {
			t.Fatalf("direct %s: forwarded=%v (delivered=%v)", kind, v, ok)
		}
	}
	got = map[string]bool{}
	send(10, true)
	for _, kind := range []string{"voice", "photo", "document"} {
		if !got[kind] {
			t.Fatalf("forwarded %s reported as the sender's own", kind)
		}
	}
}
