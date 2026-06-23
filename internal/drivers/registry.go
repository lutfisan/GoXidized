package drivers

import (
	"fmt"
	"sort"
	"sync"

	"goxidized/pkg/goxidized"
)

type Factory func() goxidized.Driver

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

func Register(name string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = factory
}

func Get(name string) (goxidized.Driver, error) {
	mu.RLock()
	defer mu.RUnlock()
	factory, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("driver %q not registered", name)
	}
	return factory(), nil
}

func List() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func RegisterDefaults() {
	Register("cisco_iosxe", func() goxidized.Driver {
		return NewCLI("cisco_iosxe", []string{"terminal length 0"}, "show running-config")
	})
	Register("huawei_vrp", func() goxidized.Driver {
		return NewCLI("huawei_vrp", []string{"screen-length 0 temporary"}, "display current-configuration")
	})
	Register("cisco_iosxr", func() goxidized.Driver {
		return NewCLI("cisco_iosxr", []string{"terminal length 0"}, "show running-config")
	})
	Register("juniper_junos", func() goxidized.Driver {
		return NewCLI("juniper_junos", nil, "show configuration | display set")
	})
	Register("ericsson_ipos", func() goxidized.Driver {
		return NewCLI("ericsson_ipos", nil, "show configuration")
	})
	Register("zte_zxr10", func() goxidized.Driver {
		return NewCLI("zte_zxr10", []string{"terminal length 0"}, "show running-config")
	})
}
