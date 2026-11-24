package commands

import "testing"

func TestStrict(t *testing.T) {
	for in, want := range map[string]string{
		"false\necho ok\n":                  "set -e\nfalse\necho ok\n",
		"#!/bin/bash\nfalse\n":              "#!/bin/bash\nset -e\nfalse\n",
		"#!/usr/bin/env bash\nfalse\n":      "#!/usr/bin/env bash\nset -e\nfalse\n",
		"#!/bin/sh":                         "#!/bin/sh\nset -e\n",
		"#!/usr/bin/python3\nprint('x')\n":  "#!/usr/bin/python3\nprint('x')\n",
		"#!/usr/bin/env python3\nprint()\n": "#!/usr/bin/env python3\nprint()\n",
	} {
		if got := Strict(in); got != want {
			t.Errorf("Strict(%q) = %q, wil %q", in, got, want)
		}
	}
}
