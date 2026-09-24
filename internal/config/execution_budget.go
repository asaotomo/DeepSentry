package config

// ExecutionBudgetConfig separates a revisable planning estimate from resource
// ceilings. All workers in one run share TotalSteps; a model cannot raise it.
type ExecutionBudgetConfig struct {
	Enabled          bool `mapstructure:"enabled"`
	MaxSteps         int  `mapstructure:"max_steps"`
	SubagentMaxSteps int  `mapstructure:"subagent_max_steps"`
	TotalSteps       int  `mapstructure:"total_steps"`
	ExtensionSteps   int  `mapstructure:"extension_steps"`
	NoProgressSteps  int  `mapstructure:"no_progress_steps"`
}

func (c ExecutionBudgetConfig) Normalized() ExecutionBudgetConfig {
	if c.MaxSteps <= 0 {
		c.MaxSteps = 180
	}
	if c.SubagentMaxSteps <= 0 {
		c.SubagentMaxSteps = 90
	}
	if c.TotalSteps <= 0 {
		c.TotalSteps = 300
	}
	if c.ExtensionSteps <= 0 {
		c.ExtensionSteps = 15
	}
	if c.NoProgressSteps <= 0 {
		c.NoProgressSteps = 8
	}
	return c
}
