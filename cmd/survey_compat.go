package main

import (
	"ai-edr/internal/ui"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/core"
)

func configureSurveyCompatibility() {
	if !ui.ColorEnabled() {
		core.DisableColor = true
	}
}

func surveyOpts(extra ...survey.AskOpt) []survey.AskOpt {
	opts := make([]survey.AskOpt, 0, len(extra)+1)
	if ui.PlainTextMode() {
		opts = append(opts, survey.WithIcons(func(icons *survey.IconSet) {
			icons.Error.Text = "x"
			icons.Error.Format = "default"
			icons.Help.Text = "i"
			icons.Help.Format = "default"
			icons.Question.Text = "?"
			icons.Question.Format = "default"
			icons.MarkedOption.Text = "[x]"
			icons.MarkedOption.Format = "default"
			icons.UnmarkedOption.Text = "[ ]"
			icons.UnmarkedOption.Format = "default"
			icons.SelectFocus.Text = ">"
			icons.SelectFocus.Format = "default"
		}))
	}
	opts = append(opts, extra...)
	return opts
}

func askOne(prompt survey.Prompt, response interface{}, extra ...survey.AskOpt) error {
	restore := useSurveyConsoleInput()
	defer restore()
	return survey.AskOne(prompt, response, surveyOpts(extra...)...)
}

func ask(questions []*survey.Question, response interface{}, extra ...survey.AskOpt) error {
	restore := useSurveyConsoleInput()
	defer restore()
	return survey.Ask(questions, response, surveyOpts(extra...)...)
}
