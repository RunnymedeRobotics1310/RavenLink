package ntclient

import (
	"reflect"
	"testing"
)

// TestNormalizeTopicName pins the AdvantageScope-style leading-slash
// treatment and the double-slash collapse in one go.
func TestNormalizeTopicName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/1310/OdoDebug/Pose", "/1310/OdoDebug/Pose"},     // already canonical
		{"1310/OdoDebug/Pose", "/1310/OdoDebug/Pose"},      // missing leading /
		{"/1310/OdoDebug//Sub/k", "/1310/OdoDebug/Sub/k"},  // double slash collapses
		{"1310//OdoDebug/Pose", "/1310/OdoDebug/Pose"},     // both fixes in one name
		{"/", "/"},                                         // root stays root
		{"", ""},                                           // empty stays empty
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := normalizeTopicName(c.in); got != c.want {
				t.Errorf("normalizeTopicName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestExpandSlashlessPrefixVariants verifies that every "/foo" prefix
// also yields a "foo" variant so the NT4 server's literal-prefix match
// catches slashless publishers.
func TestExpandSlashlessPrefixVariants(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "single slash prefix expands",
			in:   []string{"/1310"},
			want: []string{"/1310", "1310"},
		},
		{
			name: "multiple prefixes preserve order",
			in:   []string{"/FMSInfo/", "/.schema/", "/1310/"},
			want: []string{"/FMSInfo/", "FMSInfo/", "/.schema/", ".schema/", "/1310/", "1310/"},
		},
		{
			name: "slashless input passes through unchanged",
			in:   []string{"1310/foo"},
			want: []string{"1310/foo"},
		},
		{
			name: "duplicates deduped",
			in:   []string{"/1310", "1310"},
			want: []string{"/1310", "1310"},
		},
		{
			name: "root / expands to empty string (matches everything)",
			in:   []string{"/"},
			want: []string{"/", ""},
		},
		{
			name: "empty input returns empty",
			in:   nil,
			want: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := expandSlashlessPrefixVariants(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("expandSlashlessPrefixVariants(%v)\n  got:  %v\n  want: %v", c.in, got, c.want)
			}
		})
	}
}
