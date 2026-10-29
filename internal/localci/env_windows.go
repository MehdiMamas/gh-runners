package localci

import "golang.org/x/sys/windows/registry"

func readRegistryEnv(root registry.Key, path string) (map[string]string, error) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadValueNames(0)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, n := range names {
		v, typ, err := k.GetStringValue(n)
		if err != nil {
			continue
		}
		if typ == registry.EXPAND_SZ {
			if e, err := registry.ExpandString(v); err == nil {
				v = e
			}
		}
		m[n] = v
	}
	return m, nil
}

// freshEnv overlays the machine and user environment from the registry so a
// new install is visible without a logoff or daemon restart.
func freshEnv(base []string) []string {
	machine, err := readRegistryEnv(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`)
	if err != nil {
		return base
	}
	user, err := readRegistryEnv(registry.CURRENT_USER, `Environment`)
	if err != nil {
		return base
	}
	return mergeRegistryEnv(base, machine, user)
}
