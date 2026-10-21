package config

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// EnvPrefix starts the environment variables that override config.toml. A
// key's variable is its path in upper case joined by underscores, so
// decision.thresholds.category is LEDGER_SNAP_DECISION_THRESHOLDS_CATEGORY.
const EnvPrefix = "LEDGER_SNAP_"

// deployVars share the prefix but belong to compose.yaml, not the program.
var deployVars = []string{"LEDGER_SNAP_IMAGE", "LEDGER_SNAP_PORT", "LEDGER_SNAP_UID", "LEDGER_SNAP_GID"}

var durationType = reflect.TypeFor[Duration]()

// Field is one config key with the variable that overrides it.
type Field struct {
	Key     string // decision.thresholds.category
	Env     string
	Value   string
	FromEnv bool
}

// Fields lists every key of c in file order.
func Fields(c *Config) []Field {
	var out []Field
	walk(reflect.ValueOf(c).Elem(), "", func(key string, f reflect.Value) {
		out = append(out, Field{Key: key, Env: envName(key), Value: format(f), FromEnv: slices.Contains(c.EnvOverrides, key)})
	})
	return out
}

func envName(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// walk calls fn for every leaf field with its dotted TOML key.
func walk(v reflect.Value, prefix string, fn func(key string, f reflect.Value)) {
	t := v.Type()
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}
		f := v.Field(i)
		if f.Kind() == reflect.Struct && f.Type() != durationType {
			walk(f, key, fn)
			continue
		}
		fn(key, f)
	}
}

// applyEnv overrides keys from the environment. An empty variable counts as
// unset. An unknown LEDGER_SNAP_ variable is an error, like an unknown key in
// the file, so a misspelt override is not silently ignored.
func (c *Config) applyEnv() error {
	known := map[string]bool{}
	for _, name := range deployVars {
		known[name] = true
	}
	var errs []string
	walk(reflect.ValueOf(c).Elem(), "", func(key string, f reflect.Value) {
		name := envName(key)
		known[name] = true
		if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
			if err := set(f, raw); err != nil {
				errs = append(errs, fmt.Sprintf("%s=%q：%v", name, raw, err))
			} else {
				c.EnvOverrides = append(c.EnvOverrides, key)
			}
		}
		// Secrets and invite codes may live in prefixed variables too.
		if strings.HasSuffix(key, "_env") {
			known[f.String()] = true
		}
	})
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, EnvPrefix) && !known[name] {
			errs = append(errs, "没有对应配置项的环境变量 "+name)
		}
	}
	if len(errs) > 0 {
		slices.Sort(errs)
		return fmt.Errorf("用环境变量覆盖配置失败：%s", strings.Join(errs, "；"))
	}
	return nil
}

func set(f reflect.Value, raw string) error {
	if f.Type() == durationType {
		return f.Addr().Interface().(*Duration).UnmarshalText([]byte(raw))
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString(raw)
	case reflect.Bool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("应为 true 或 false")
		}
		f.SetBool(v)
	case reflect.Int, reflect.Int64:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("应为整数")
		}
		f.SetInt(v)
	case reflect.Float64:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("应为数字")
		}
		f.SetFloat(v)
	case reflect.Slice: // []string, comma separated
		var items []string
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				items = append(items, s)
			}
		}
		f.Set(reflect.ValueOf(items))
	default:
		return fmt.Errorf("不支持的类型 %s", f.Type())
	}
	return nil
}

func format(f reflect.Value) string {
	switch {
	case f.Type() == durationType:
		return f.Interface().(Duration).String()
	case f.Kind() == reflect.Slice:
		return strings.Join(f.Interface().([]string), ",")
	}
	return fmt.Sprint(f.Interface())
}
