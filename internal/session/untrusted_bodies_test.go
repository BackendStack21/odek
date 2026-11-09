package session

import (
	"strings"
	"testing"
)

func TestStripUntrustedBodies_Table(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unchanged", "plain text", "plain text"},
		{"prose naming the tag", "what does <untrusted_content_ mean", "what does <untrusted_content_ mean"},
		{"non-hex nonce is not a wrapper", `a <untrusted_content_zz source="x">b`, `a <untrusted_content_zz source="x">b`},
		{"fence without a nonce is not a fence", "a <untrusted_input_> b", "a <untrusted_input_> b"},
		{
			"two wrappers",
			`pre <untrusted_content_ab source="w">` + "\nONE\n" + `</untrusted_content_ab> mid <untrusted_content_cd source="r">TWO</untrusted_content_cd> post`,
			"pre \n mid \n post",
		},
		{
			"unterminated wrapper runs to the end",
			`pre <untrusted_content_ab source="w">` + "\nBODY and the rest",
			"pre \n",
		},
		{
			"unterminated label runs to the end",
			`pre <untrusted_content_ab source="w BODY`,
			"pre \n",
		},
		{
			"close tag with the wrong nonce does not end the wrapper",
			`pre <untrusted_content_ab source="w">ONE</untrusted_content_cd> TWO</untrusted_content_ab> post`,
			"pre \n post",
		},
		{
			"close tag with the wrong nonce and no right one runs to the end",
			`pre <untrusted_content_ab source="w">ONE</untrusted_content_cd> post`,
			"pre \n",
		},
		{
			"nested look-alike wrapper",
			`pre <untrusted_content_ab source="w">ONE <untrusted_content_cd source="x">TWO</untrusted_content_cd> THREE</untrusted_content_ab> post`,
			"pre \n post",
		},
		{
			"fence",
			"pre <untrusted_input_ab12>\nBODY\n</untrusted_input_ab12> post",
			"pre \n post",
		},
		{
			"unterminated fence runs to the end",
			"pre <untrusted_input_ab12>\nBODY",
			"pre \n",
		},
		{
			"fence nested in a wrapper",
			`pre <untrusted_content_ab source="w">` + "\n<untrusted_input_cd>\nX\n</untrusted_input_cd>\n</untrusted_content_ab> post",
			"pre \n post",
		},
		{
			"wrapper and fence side by side",
			`a <untrusted_input_ab>X</untrusted_input_ab> b <untrusted_content_cd source="w">Y</untrusted_content_cd> c`,
			"a \n b \n c",
		},
		{
			"removed regions cannot join into a new tag",
			`<untrusted_con` + `<untrusted_content_ab source="w">Y</untrusted_content_ab>` + `tent_cd source="x">Z`,
			"<untrusted_con\ntent_cd source=\"x\">Z",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StripUntrustedBodies(c.in); got != c.want {
				t.Errorf("StripUntrustedBodies(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

func TestUntrustedBodies_SourcesAndOffsets(t *testing.T) {
	s := `pre <untrusted_content_ab12 source="web">` + "\nBODY1\n" + `</untrusted_content_ab12> mid ` +
		`<untrusted_content_cd34 source="ref">` + "\nBODY2 unterminated"
	got := UntrustedBodies(s)
	if len(got) != 2 {
		t.Fatalf("bodies = %+v", got)
	}
	if got[0].Source != "web" || got[0].Body != "\nBODY1\n" || s[got[0].Start:got[0].End] != `<untrusted_content_ab12 source="web">`+"\nBODY1\n"+`</untrusted_content_ab12>` {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Source != "ref" || !strings.Contains(got[1].Body, "BODY2") || got[1].End != len(s) {
		t.Errorf("second = %+v", got[1])
	}
	if got := UntrustedBodies(`<untrusted_content_ab source="unclosed label`); len(got) != 1 || got[0].Source != "" || got[0].Body != "unclosed label" {
		t.Errorf("malformed label = %+v", got)
	}

	in := UntrustedInputBodies("a <untrusted_input_ab12>X</untrusted_input_ab12> b <untrusted_input_cd>Y")
	if len(in) != 2 || in[0].Body != "X" || in[0].Source != "" || in[1].Body != "Y" {
		t.Errorf("fences = %+v", in)
	}
}
