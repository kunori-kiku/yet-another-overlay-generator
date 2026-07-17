package telemetrycap

import (
	"reflect"
	"testing"
)

func TestCapabilityContractHasUniqueDeterministicInstallerMarkers(t *testing.T) {
	want := []string{
		InstallerDeviceV1Env,
		InstallerPolicyV1Env,
		InstallerPolicyV2Env,
		InstallerURLV1Env,
	}
	if got := InstallerEnvironments(); !reflect.DeepEqual(got, want) {
		t.Fatalf("installer environments = %v, want %v", got, want)
	}
	for _, token := range []string{PolicyV1, PolicyV2, URLV1, DeviceV1} {
		definition, ok := Lookup(token)
		if !ok || definition.Token != token || definition.InstallerEnvironment == "" || definition.InstallerError == "" {
			t.Fatalf("capability %q has incomplete launcher definition: %+v, found=%v", token, definition, ok)
		}
	}
	if _, ok := Lookup("future-unmapped-capability"); ok {
		t.Fatal("unknown capability unexpectedly has a launcher definition")
	}
}
