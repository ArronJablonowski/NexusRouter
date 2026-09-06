package resources

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func thermalFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func assertThermal(t *testing.T, actual *bool, want string) {
	t.Helper()
	if want == "unknown" {
		if actual != nil {
			t.Fatalf("pressure = %v, want unknown", *actual)
		}
		return
	}
	if actual == nil || *actual != (want == "pressure") {
		t.Fatalf("pressure = %v, want %s", actual, want)
	}
}

func TestLinuxThermalThresholdEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, temperature, tripType, threshold, want string
	}{
		{"passive below", "79999\n", "passive\n", "80000\n", "clear"},
		{"passive equality", "80000", "passive", "80000", "pressure"},
		{"passive above", "80001", "passive", "80000", "pressure"},
		{"hot equality", "90000", "hot", "90000", "pressure"},
		{"critical equality", "100000", "critical", "100000", "pressure"},
		{"critical below is incomplete", "70000", "critical", "100000", "unknown"},
		{"hot below is incomplete", "70000", "hot", "90000", "unknown"},
		{"fan trip is not pressure", "80000", "active0", "60000", "unknown"},
		{"unknown trip", "80000", "vendor", "60000", "unknown"},
		{"zero threshold disabled", "80000", "passive", "0", "unknown"},
		{"negative threshold", "80000", "passive", "-1", "unknown"},
		{"negative current valid", "-1000", "passive", "80000", "clear"},
		{"absolute zero boundary", "-273150", "passive", "80000", "clear"},
		{"below absolute zero", "-273151", "passive", "80000", "unknown"},
		{"current int32 maximum", "2147483647", "passive", "80000", "pressure"},
		{"current int32 overflow", "2147483648", "passive", "80000", "unknown"},
		{"current invalid sentinel", "-2147483648", "passive", "80000", "unknown"},
		{"threshold overflow", "80000", "passive", "2147483648", "unknown"},
		{"current malformed", "80000oops", "passive", "80000", "unknown"},
		{"threshold malformed", "80000", "passive", "8e4", "unknown"},
		{"empty temperature", "", "passive", "80000", "unknown"},
		{"multiple temperatures", "70000\n80000", "passive", "80000", "unknown"},
		{"oversized scalar", strings.Repeat("7", 65), "passive", "80000", "unknown"},
		{"multiple terminal newlines", "70000\n\n", "passive", "80000", "unknown"},
		{"leading whitespace", " 70000", "passive", "80000", "unknown"},
		{"positive sign is noncanonical", "+70000", "passive", "80000", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := thermalFixture(t, map[string]string{
				"thermal_zone0/temp":              tc.temperature,
				"thermal_zone0/trip_point_0_type": tc.tripType,
				"thermal_zone0/trip_point_0_temp": tc.threshold,
			})
			assertThermal(t, probeLinuxThermal(context.Background(), root), tc.want)
		})
	}
}

func TestLinuxThermalIncompleteAndMixedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"empty root", nil, "unknown"},
		{"no thermal zones", map[string]string{"cooling_device0/cur_state": "10"}, "unknown"},
		{"missing temperature", map[string]string{"thermal_zone0/trip_point_0_type": "passive", "thermal_zone0/trip_point_0_temp": "80000"}, "unknown"},
		{"missing type", map[string]string{"thermal_zone0/temp": "90000", "thermal_zone0/trip_point_0_temp": "80000"}, "unknown"},
		{"missing threshold", map[string]string{"thermal_zone0/temp": "90000", "thermal_zone0/trip_point_0_type": "passive"}, "unknown"},
		{"sparse canonical indices", map[string]string{"thermal_zone42/temp": "70000", "thermal_zone42/trip_point_23_type": "passive", "thermal_zone42/trip_point_23_temp": "80000"}, "clear"},
		{"uint32 maximum indices", map[string]string{"thermal_zone4294967295/temp": "70000", "thermal_zone4294967295/trip_point_4294967295_type": "passive", "thermal_zone4294967295/trip_point_4294967295_temp": "80000"}, "clear"},
		{"unsupported zone prevents clear", map[string]string{"thermal_zone0/temp": "70000", "thermal_zone0/trip_point_0_type": "passive", "thermal_zone0/trip_point_0_temp": "80000", "thermal_zone1/temp": "70000"}, "unknown"},
		{"hot alongside unknown zone", map[string]string{"thermal_zone0/temp": "90000", "thermal_zone0/trip_point_0_type": "hot", "thermal_zone0/trip_point_0_temp": "90000", "thermal_zone1/temp": "bad"}, "pressure"},
		{"unknown type prevents clear", map[string]string{"thermal_zone0/temp": "70000", "thermal_zone0/trip_point_0_type": "passive", "thermal_zone0/trip_point_0_temp": "80000", "thermal_zone0/trip_point_1_type": "vendor", "thermal_zone0/trip_point_1_temp": "90000"}, "unknown"},
		{"critical crosses despite broken passive", map[string]string{"thermal_zone0/temp": "100000", "thermal_zone0/trip_point_0_type": "passive", "thermal_zone0/trip_point_0_temp": "bad", "thermal_zone0/trip_point_1_type": "critical", "thermal_zone0/trip_point_1_temp": "100000"}, "pressure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertThermal(t, probeLinuxThermal(context.Background(), thermalFixture(t, tc.files)), tc.want)
		})
	}
}

func TestLinuxThermalRejectsNoncanonicalNames(t *testing.T) {
	for _, zone := range []string{"thermal_zone01", "thermal_zone-1", "thermal_zone+1", "thermal_zone4294967296", "thermal_zone1x"} {
		t.Run(zone, func(t *testing.T) {
			root := thermalFixture(t, map[string]string{zone + "/temp": "90000", zone + "/trip_point_0_type": "passive", zone + "/trip_point_0_temp": "80000"})
			assertThermal(t, probeLinuxThermal(context.Background(), root), "unknown")
		})
	}
	for _, index := range []string{"01", "-1", "+1", "4294967296", "1x"} {
		t.Run("trip "+index, func(t *testing.T) {
			root := thermalFixture(t, map[string]string{"thermal_zone0/temp": "90000", "thermal_zone0/trip_point_" + index + "_type": "passive", "thermal_zone0/trip_point_" + index + "_temp": "80000"})
			assertThermal(t, probeLinuxThermal(context.Background(), root), "unknown")
		})
	}
}

func TestLinuxThermalFilesystemAndCancellation(t *testing.T) {
	root := thermalFixture(t, map[string]string{"thermal_zone0/temp": "70000", "thermal_zone0/trip_point_0_type": "passive", "thermal_zone0/trip_point_0_temp": "80000"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertThermal(t, probeLinuxThermal(ctx, root), "unknown")
	assertThermal(t, probeLinuxThermal(nil, root), "unknown")
	assertThermal(t, probeLinuxThermal(context.Background(), filepath.Join(root, "absent")), "unknown")
	assertThermal(t, probeLinuxThermal(context.Background(), filepath.Join(root, "thermal_zone0/temp")), "unknown")
	linkRoot := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "thermal_zone0"), filepath.Join(linkRoot, "thermal_zone0")); err != nil {
		t.Fatal(err)
	}
	assertThermal(t, probeLinuxThermal(context.Background(), linkRoot), "clear")
	if err := os.Remove(filepath.Join(root, "thermal_zone0/temp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "thermal_zone0/temp"), 0700); err != nil {
		t.Fatal(err)
	}
	assertThermal(t, probeLinuxThermal(context.Background(), root), "unknown")
}

func TestLinuxThermalDiscoveryBounds(t *testing.T) {
	for _, tc := range []struct {
		name                string
		zones, trips, extra int
	}{
		{"zone count overflow", 65, 1, 0},
		{"trip count overflow", 1, 65, 0},
		{"directory entry overflow", 1, 1, 257},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			for z := 0; z < tc.zones; z++ {
				base := fmt.Sprintf("thermal_zone%d/", z)
				files[base+"temp"] = "70000"
				for trip := 0; trip < tc.trips; trip++ {
					files[base+fmt.Sprintf("trip_point_%d_type", trip)] = "passive"
					files[base+fmt.Sprintf("trip_point_%d_temp", trip)] = "80000"
				}
			}
			for i := 0; i < tc.extra; i++ {
				files[fmt.Sprintf("other%d", i)] = ""
			}
			assertThermal(t, probeLinuxThermal(context.Background(), thermalFixture(t, files)), "unknown")
		})
	}
}

func TestLinuxThermalExactLimitsAndIrrelevantNodes(t *testing.T) {
	for _, n := range []int{1, 64} {
		t.Run(fmt.Sprintf("%d zones and trips", n), func(t *testing.T) {
			files := map[string]string{}
			for z := 0; z < n; z++ {
				base := fmt.Sprintf("thermal_zone%d/", z)
				files[base+"temp"] = "70000"
				for trip := 0; trip < n; trip++ {
					files[base+fmt.Sprintf("trip_point_%d_type", trip)] = "passive"
					files[base+fmt.Sprintf("trip_point_%d_temp", trip)] = "80000"
				}
				// Invalid contents in unrelated attributes must not taint evidence.
				for _, name := range []string{"mode", "policy", "emul_temp", "trip_point_0_hyst", "cdev0_trip_point"} {
					files[base+name] = strings.Repeat("invalid", 100)
				}
			}
			assertThermal(t, probeLinuxThermal(context.Background(), thermalFixture(t, files)), "clear")
		})
	}
}
