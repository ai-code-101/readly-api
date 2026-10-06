package slug

import "testing"

func TestMake(t *testing.T) {
	cases := map[string]string{
		"The Glass House":         "the-glass-house",
		"Children's Literature":   "childrens-literature",
		"Children’s Literature":   "childrens-literature",
		"  Décisions & Dynamics!": "decisions-dynamics",
		"Sci-Fi":                  "sci-fi",
		"!!!":                     "untitled",
	}
	for in, want := range cases {
		if got := Make(in); got != want {
			t.Errorf("Make(%q) = %q, want %q", in, got, want)
		}
	}
}
