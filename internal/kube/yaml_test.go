package kube

import "testing"

func TestToYAML(t *testing.T) {
	body := []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"app","labels":{"z":"1","a":"yes"}},` +
		`"data":{"poort":"8080","script":"#!/bin/sh\necho hoi\n","leeg":""},"n":3,"f":1.5,"on":true,"x":null,"lijst":[1,"twee"]}`)
	got, err := ToYAML(body)
	if err != nil {
		t.Fatal(err)
	}
	want := `apiVersion: v1
kind: ConfigMap
metadata:
  name: app
  labels:
    z: "1"
    a: "yes"
data:
  poort: "8080"
  script: |
    #!/bin/sh
    echo hoi
  leeg: ""
"n": 3
f: 1.5
"on": true
x: null
lijst:
  - 1
  - twee
`
	if got != want {
		t.Fatalf("YAML:\n%s\nverwacht:\n%s", got, want)
	}
}
