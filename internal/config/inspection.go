package config

// Inspection profiles are operator-controlled; credentials are resolved at run
// time from environment variables or existing named target definitions.
type InspectionConfig struct {
	OutputDir        string             `mapstructure:"output_dir"`
	Concurrency      int                `mapstructure:"concurrency"`
	DeviceTimeoutSec int                `mapstructure:"device_timeout_sec"`
	Devices          []InspectionDevice `mapstructure:"devices"`
}
type InspectionDevice struct {
	Name             string            `mapstructure:"name"`
	Tags             []string          `mapstructure:"tags"`
	Target           string            `mapstructure:"target"`
	URL              string            `mapstructure:"url"`
	UsernameEnv      string            `mapstructure:"username_env"`
	PasswordEnv      string            `mapstructure:"password_env"`
	UsernameSelector string            `mapstructure:"username_selector"`
	PasswordSelector string            `mapstructure:"password_selector"`
	SubmitSelector   string            `mapstructure:"submit_selector"`
	ReadySelector    string            `mapstructure:"ready_selector"`
	Checks           []InspectionCheck `mapstructure:"checks"`
}
type InspectionCheck struct {
	Name         string   `mapstructure:"name"`
	Command      string   `mapstructure:"command"`
	URL          string   `mapstructure:"url"`
	Selector     string   `mapstructure:"selector"`
	Screenshot   bool     `mapstructure:"screenshot"`
	ExpectRegex  string   `mapstructure:"expect_regex"`
	AlertRegex   string   `mapstructure:"alert_regex"`
	NumericRegex string   `mapstructure:"numeric_regex"`
	MaxValue     *float64 `mapstructure:"max_value"`
}
