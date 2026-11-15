package main

import (
	"reflect"
	"testing"
)

func TestUpdateIncludesOnlyEnabledMediaWorkers(t *testing.T) {
	enabled := func(name string) bool { return name == "checkpoint-recorder" }
	if got := updateServiceNames([]string{"checkpoint-core", "checkpoint-signal"}, enabled); !reflect.DeepEqual(got, []string{"checkpoint-core", "checkpoint-signal", "checkpoint-recorder"}) {
		t.Fatal(got)
	}
	if got := updateServiceNames([]string{"checkpoint-observer"}, enabled); !reflect.DeepEqual(got, []string{"checkpoint-observer"}) {
		t.Fatal(got)
	}
}
