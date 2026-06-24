package conformance

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goxidized/internal/drivers"
	"goxidized/pkg/goxidized"
)

type requiredVendorFixture struct {
	vendor        string
	fetch         string
	prepare       []string
	secrets       []string
	errorCategory goxidized.FailureCategory
}

var requiredVendorFixtures = []requiredVendorFixture{
	{
		vendor:        "cisco_iosxe",
		fetch:         "show running-config",
		prepare:       []string{"terminal length 0", "terminal width 0"},
		secrets:       []string{"iosxeEnable9", "iosxeUser9", "iosxePublic", "iosxeTacacs"},
		errorCategory: goxidized.FailureCommand,
	},
	{
		vendor:        "huawei_vrp",
		fetch:         "display current-configuration",
		prepare:       []string{"screen-length 0 temporary"},
		secrets:       []string{"huaweiSuper", "huaweiLocal", "huaweiPublic", "huaweiTacacs"},
		errorCategory: goxidized.FailureCommand,
	},
	{
		vendor:        "cisco_iosxr",
		fetch:         "show running-config",
		prepare:       []string{"terminal length 0", "terminal width 0"},
		secrets:       []string{"iosxrUser10", "iosxrPublic", "iosxrTacacs"},
		errorCategory: goxidized.FailurePrivilege,
	},
	{
		vendor:        "juniper_junos",
		fetch:         "show configuration | display set | no-more",
		prepare:       []string{"set cli screen-length 0", "set cli screen-width 0"},
		secrets:       []string{"$6$junosUser", "$6$junosRoot", "junosPublic", "junosRadius", "junosBgpKey"},
		errorCategory: goxidized.FailurePrivilege,
	},
	{
		vendor:        "zte_zxr10",
		fetch:         "show running-config",
		prepare:       []string{"terminal length 0"},
		secrets:       []string{"zteEnable5", "zteUser0", "ztePublic", "zteTacacs"},
		errorCategory: goxidized.FailurePrivilege,
	},
	{
		vendor:        "ericsson_ipos",
		fetch:         "show configuration",
		prepare:       []string{"terminal length 0", "terminal width 0"},
		secrets:       []string{"iposUserHash", "iposPublic", "iposRadiusKey"},
		errorCategory: goxidized.FailurePrivilege,
	},
}

func TestRequiredDriversRegistered(t *testing.T) {
	drivers.RegisterDefaults()
	for _, tc := range requiredVendorFixtures {
		t.Run(tc.vendor, func(t *testing.T) {
			driver, err := drivers.Get(tc.vendor)
			if err != nil {
				t.Fatal(err)
			}
			if driver.Vendor() != tc.vendor {
				t.Fatalf("driver vendor=%q, want %q", driver.Vendor(), tc.vendor)
			}
		})
	}
}

func TestRequiredVendorFixturesNormalizeAndRedact(t *testing.T) {
	drivers.RegisterDefaults()
	for _, tc := range requiredVendorFixtures {
		t.Run(tc.vendor, func(t *testing.T) {
			driver, err := drivers.Get(tc.vendor)
			if err != nil {
				t.Fatal(err)
			}
			transcript := readFixture(t, tc.vendor, "normal.synthetic.transcript")
			expectedNormalized := readFixture(t, tc.vendor, "normal.expected.cfg")
			expectedRedacted := readFixture(t, tc.vendor, "normal.expected.redacted.cfg")
			run, err := RunDriverFixtureDetailed(context.Background(), driver, DriverFixture{
				Target:           targetFor(tc.vendor),
				Responses:        responsesFor(tc, transcript),
				ExpectedCommand:  tc.fetch,
				ExpectedCommands: expectedCommands(tc),
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := string(run.Normalized.RawConfig); got != string(expectedNormalized) {
				t.Fatalf("normalized config mismatch\n--- got ---\n%s--- want ---\n%s", got, expectedNormalized)
			}
			if got := string(run.Redacted.Content); got != string(expectedRedacted) {
				t.Fatalf("redacted config mismatch\n--- got ---\n%s--- want ---\n%s", got, expectedRedacted)
			}
			for _, secret := range tc.secrets {
				if strings.Contains(string(run.Redacted.Content), secret) {
					t.Fatalf("seeded secret %q leaked in redacted output:\n%s", secret, run.Redacted.Content)
				}
			}
			if run.Report.SecretsFound == 0 {
				t.Fatalf("expected redaction report")
			}
		})
	}
}

func TestRequiredVendorMarkerFixturesClassify(t *testing.T) {
	drivers.RegisterDefaults()
	markers := []struct {
		name     string
		file     string
		category func(requiredVendorFixture) goxidized.FailureCategory
	}{
		{name: "error", file: "error.synthetic.transcript", category: func(tc requiredVendorFixture) goxidized.FailureCategory { return tc.errorCategory }},
		{name: "timeout", file: "timeout.synthetic.transcript", category: func(requiredVendorFixture) goxidized.FailureCategory { return goxidized.FailureTimeout }},
		{name: "oversized", file: "oversized.synthetic.transcript", category: func(requiredVendorFixture) goxidized.FailureCategory { return goxidized.FailureCommand }},
	}
	for _, tc := range requiredVendorFixtures {
		for _, marker := range markers {
			t.Run(tc.vendor+"/"+marker.name, func(t *testing.T) {
				driver, err := drivers.Get(tc.vendor)
				if err != nil {
					t.Fatal(err)
				}
				run, err := RunDriverFixtureDetailed(context.Background(), driver, DriverFixture{
					Target:    targetFor(tc.vendor),
					Responses: responsesFor(tc, readFixture(t, tc.vendor, marker.file)),
				})
				if err == nil {
					t.Fatalf("expected %s marker to fail", marker.name)
				}
				wantCategory := marker.category(tc)
				if got := goxidized.ClassifyError(err); got != wantCategory {
					t.Fatalf("category=%s, want %s; err=%v", got, wantCategory, err)
				}
				if !reflect.DeepEqual(run.Commands, expectedCommands(tc)) {
					t.Fatalf("commands=%q, want %q", run.Commands, expectedCommands(tc))
				}
			})
		}
	}
}

func readFixture(t *testing.T, vendor, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", vendor, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func responsesFor(tc requiredVendorFixture, fetch []byte) map[string][]byte {
	responses := map[string][]byte{tc.fetch: fetch}
	for _, cmd := range tc.prepare {
		responses[cmd] = nil
	}
	return responses
}

func expectedCommands(tc requiredVendorFixture) []string {
	out := append([]string(nil), tc.prepare...)
	return append(out, tc.fetch)
}

func targetFor(vendor string) goxidized.Target {
	return goxidized.Target{
		ID:            vendor + "-fixture",
		Hostname:      vendor + "-fixture",
		IPAddress:     "192.0.2.10",
		Vendor:        vendor,
		Group:         "conformance",
		CredentialRef: "dotenv://" + strings.ToUpper(vendor),
		Enabled:       true,
	}
}
