package config

import (
	"fmt"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// LoadFile reads a YAML config from path, applies locked defaults, validates,
// and returns the Config. Any error is a startup-failing boundary error;
// callers should fatal-abort (per ROADMAP "失敗 fatal abort 不降級").
func LoadFile(path string) (*Config, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("config: load %q: %w", path, err)
	}

	var c Config
	if err := k.UnmarshalWithConf("", &c, koanf.UnmarshalConf{
		Tag: "koanf",
		DecoderConfig: &mapstructure.DecoderConfig{
			Result:           &c,
			TagName:          "koanf",
			WeaklyTypedInput: true,
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
			),
		},
	}); err != nil {
		return nil, fmt.Errorf("config: unmarshal: %w", err)
	}

	c.applyDefaults()

	v := validator.New(validator.WithRequiredStructEnabled())
	if err := v.Struct(&c); err != nil {
		return nil, fmt.Errorf("config: validate: %w", err)
	}
	return &c, nil
}
