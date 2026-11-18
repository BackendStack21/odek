package main

import "testing"

// The model picker labels context windows the way providers advertise them:
// decimal windows in thousands (128000 → 128K), power-of-two windows in
// binary units (131072 → 128K) and whole millions as M. A 1024 divisor
// showed 128000 as "125K" and 32000 as "31K".
func TestRED_ModelListContextSizesUseAdvertisedUnits(t *testing.T) {
	cases := []struct {
		maxCtx int
		want   string
	}{
		{128000, "m — 128K ctx"},
		{32000, "m — 32K ctx"},
		{200000, "m — 200K ctx"},
		{64000, "m — 64K ctx"},
		{131072, "m — 128K ctx"},
		{32768, "m — 32K ctx"},
		{163840, "m — 160K ctx"},
		{8192, "m — 8K ctx"},
		{1_000_000, "m — 1M ctx"},
		{2_000_000, "m — 2M ctx"},
		{1_048_576, "m — 1M ctx"},
		{127_999, "m — 128K ctx"},
		{512, "m — 512 ctx"},
		{0, "m"},
	}
	for _, tc := range cases {
		if got := modelListEntry("m", "", tc.maxCtx, false).Description; got != tc.want {
			t.Errorf("max_context %d: description %q, want %q", tc.maxCtx, got, tc.want)
		}
	}
}
