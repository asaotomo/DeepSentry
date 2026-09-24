package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCatalogExpandsHomeSkillSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	skillDir := filepath.Join(home, ".deepsentry", "skills", "external-audit")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	content := `---
name: external-audit
description: 外部审计 Skill
license: Apache-2.0
---

# External Audit
`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	catalog, err := LoadCatalog([]string{"~/.deepsentry/skills"})
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	meta, ok := catalog.FindSkill("external-audit")
	if !ok {
		t.Fatalf("expected external-audit skill, got %#v", catalog.Skills)
	}
	if meta.Description != "外部审计 Skill" {
		t.Fatalf("unexpected description: %q", meta.Description)
	}
}

func TestLoadCatalogHonorsClaudeAndCodexInvocationPolicies(t *testing.T) {
	root := t.TempDir()
	writeSkill := func(dir, frontmatter, openAI string) {
		t.Helper()
		path := filepath.Join(root, dir)
		if err := os.MkdirAll(filepath.Join(path, "agents"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("---\n"+frontmatter+"---\n# Body\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if openAI != "" {
			if err := os.WriteFile(filepath.Join(path, "agents", "openai.yaml"), []byte(openAI), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeSkill("explicit", "name: explicit\ndescription: Explicit only\ndisable-model-invocation: true\n", "")
	writeSkill("model-only", "name: model-only\ndescription: Model only\nuser-invocable: false\n", "")
	writeSkill("codex-policy", "name: codex-policy\ndescription: Codex policy\n", "policy:\n  allow_implicit_invocation: false\n")
	writeSkill("both-policies", "name: both-policies\ndescription: Both policies\ndisable-model-invocation: true\n", "policy:\n  allow_implicit_invocation: true\n")

	catalog, err := LoadCatalog([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	explicit, ok := catalog.FindSkill("EXPLICIT")
	if !ok || explicit.AllowImplicit || !explicit.UserInvocable {
		t.Fatalf("unexpected explicit metadata: %#v", explicit)
	}
	modelOnly, ok := catalog.FindSkill("model-only")
	if !ok || !modelOnly.AllowImplicit || modelOnly.UserInvocable {
		t.Fatalf("unexpected model-only metadata: %#v", modelOnly)
	}
	codex, ok := catalog.FindSkill("codex-policy")
	if !ok || codex.AllowImplicit || codex.InvocationSource != "agents/openai.yaml" {
		t.Fatalf("unexpected Codex policy metadata: %#v", codex)
	}
	both, ok := catalog.FindSkill("both-policies")
	if !ok || both.AllowImplicit {
		t.Fatalf("SKILL.md opt-out was overridden by agents/openai.yaml: %#v", both)
	}
	prompt := catalog.FormatCatalogPrompt()
	if strings.Contains(prompt, "**explicit**") || strings.Contains(prompt, "**codex-policy**") || !strings.Contains(prompt, "**model-only**") {
		t.Fatalf("invocation policy not reflected in catalog prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "skill(name=") || !strings.Contains(prompt, "再动手") {
		t.Fatalf("catalog prompt should teach skill-first loading:\n%s", prompt)
	}
}

func TestFormatCatalogPromptHasBoundedProgressiveDisclosure(t *testing.T) {
	catalog := &SkillCatalog{}
	for i := 0; i < 200; i++ {
		catalog.Skills = append(catalog.Skills, SkillMeta{Name: "skill-" + strings.Repeat("x", 20), Description: strings.Repeat("detail ", 40), AllowImplicit: true})
	}
	prompt := catalog.FormatCatalogPrompt()
	if len(prompt) > 8500 {
		t.Fatalf("catalog prompt exceeded budget: %d bytes", len(prompt))
	}
	if !strings.Contains(prompt, "因目录预算未列出") {
		t.Fatalf("expected progressive disclosure notice:\n%s", prompt)
	}
}

func TestLoadCatalogRejectsMalformedSkillMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Missing frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 0 {
		t.Fatalf("malformed Skill should be skipped: %#v", catalog.Skills)
	}
}

func TestResolveSourcesAlwaysIncludesManagedRootAndHonorsDisable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	custom := filepath.Join(home, "custom-skills")
	managed := filepath.Join(home, ".deepsentry", "skills")
	sources := ResolveSources([]string{custom, custom}, nil)
	if len(sources) < 2 || sources[len(sources)-2] != custom || sources[len(sources)-1] != managed {
		t.Fatalf("resolved sources=%#v", sources)
	}
	sources = ResolveSources([]string{custom}, []string{managed})
	if len(sources) == 0 || sources[len(sources)-1] != custom {
		t.Fatalf("disabled managed root should be absent: %#v", sources)
	}
	for _, source := range sources {
		if source == managed {
			t.Fatalf("disabled managed root should be absent: %#v", sources)
		}
	}
}

func TestCatalogKeepsRelativeRootStableAcrossWorkingDirectoryChange(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "audit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: audit\ndescription: Audit\n---\n# Body\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	catalog, err := LoadCatalog([]string{"skills"})
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := catalog.Reload(); err != nil {
		t.Fatal(err)
	}
	meta, ok := catalog.FindSkill("audit")
	if !ok || !filepath.IsAbs(meta.Path) {
		t.Fatalf("relative Skill root was lost after cwd change: %#v", catalog.Skills)
	}
}

func TestDefaultSourcesIncludeBundledSkillsBesideExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	want := filepath.Join(filepath.Dir(executable), "bundled-skills")
	found := false
	for _, source := range DefaultSources() {
		if source == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("default sources do not include installed playbooks beside %s: %#v", executable, DefaultSources())
	}
}

func TestCatalogReloadDiscoversNewlyLandedSkill(t *testing.T) {
	root := t.TempDir()
	catalog, err := LoadCatalog([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 0 {
		t.Fatalf("unexpected initial skills: %#v", catalog.Skills)
	}
	dir := filepath.Join(root, "hot-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: hot-skill\ndescription: Hot reload test\n---\n# Hot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.FindSkill("hot-skill"); !ok {
		t.Fatalf("reloaded catalog=%#v", catalog.Skills)
	}
}

func TestCatalogDisabledSkillPolicySurvivesReloadAndCanBeEnabled(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		doc := "---\nname: " + name + "\ndescription: " + name + " skill\n---\n# Body\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := LoadCatalog([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	catalog.ApplyDisabledSkills([]string{"ALPHA", "alpha"})
	if !catalog.IsDisabled("Alpha") {
		t.Fatal("disabled name should be matched case-insensitively")
	}
	if _, ok := catalog.FindSkill("alpha"); ok {
		t.Fatal("disabled skill remained discoverable")
	}
	if _, ok := catalog.FindSkill("beta"); !ok {
		t.Fatal("unrelated skill was filtered")
	}
	if prompt := catalog.FormatCatalogPrompt(); strings.Contains(prompt, "**alpha**") {
		t.Fatalf("disabled skill leaked into model prompt:\n%s", prompt)
	}
	if err := catalog.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.FindSkill("alpha"); ok {
		t.Fatal("reload forgot disabled policy")
	}
	if err := catalog.ReloadWithDisabled([]string{"*"}); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 0 || !catalog.IsDisabled("beta") {
		t.Fatalf("global disable sentinel did not block every Skill: %#v", catalog.Skills)
	}
	if err := catalog.ReloadWithDisabled(nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.FindSkill("alpha"); !ok {
		t.Fatal("enabled skill was not rediscovered without restart")
	}
}

func TestBundledBilibiliPlaySkillIsLoadable(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	catalog, err := LoadCatalog([]string{root})
	if err != nil {
		t.Fatalf("load bundled skills: %v", err)
	}
	for _, name := range []string{"bilibili-play", "zipcracker", "fofamap"} {
		if _, ok := catalog.FindSkill(name); !ok {
			t.Fatalf("bundled %s skill missing; got %#v", name, catalog.Skills)
		}
	}
	meta, _ := catalog.FindSkill("bilibili-play")
	if !strings.Contains(meta.Description, "B站") && !strings.Contains(strings.ToLower(meta.Description), "bilibili") {
		t.Fatalf("bilibili-play description should mention Bilibili: %q", meta.Description)
	}
	content, err := LoadSkillContent(*meta)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"playbackRate", "press_key", "canvas", "drawImage", "?p=N"} {
		if !strings.Contains(content, want) {
			t.Fatalf("bilibili-play skill missing %q", want)
		}
	}
	zipMeta, _ := catalog.FindSkill("zipcracker")
	zipContent, err := LoadSkillContent(*zipMeta)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"zip_password_recover", "伪加密", "CRC32", "bkcrack"} {
		if !strings.Contains(zipContent, want) {
			t.Fatalf("zipcracker skill missing %q", want)
		}
	}
	fofaMeta, _ := catalog.FindSkill("fofamap")
	fofaContent, err := LoadSkillContent(*fofaMeta)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fofa_rules", "fofa_validate_query", "next_cursor", "nuclei_plan", "fofa_recon.py"} {
		if !strings.Contains(fofaContent, want) {
			t.Fatalf("fofamap skill missing %q", want)
		}
	}
}

func TestLoadCatalogPrefersMCPFofaMapOverPythonPlaybook(t *testing.T) {
	root := t.TempDir()
	bundled := filepath.Join(root, "bundled", "fofamap")
	market := filepath.Join(root, "market", "fofamap")
	if err := os.MkdirAll(filepath.Join(market, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundled, 0o755); err != nil {
		t.Fatal(err)
	}
	mcpSkill := `---
name: fofamap
description: Use FofaMap MCP tools for FOFA search. Trigger fofa_account then fofa_search.
---

# FofaMap MCP
Call fofa_account then fofa_search. Never run scripts/fofa_recon.py.
`
	pythonSkill := `---
name: fofamap
description: Run FOFA recon through python helper scripts.
---

# fofamap
Use scripts/fofa_recon.py search --query 'app="nginx"'
`
	if err := os.WriteFile(filepath.Join(bundled, "SKILL.md"), []byte(mcpSkill), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(market, "SKILL.md"), []byte(pythonSkill), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(market, "scripts", "fofa_recon.py"), []byte("print('not mcp')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog, err := LoadCatalog([]string{filepath.Join(root, "bundled"), filepath.Join(root, "market")})
	if err != nil {
		t.Fatal(err)
	}
	meta, ok := catalog.FindSkill("fofamap")
	if !ok {
		t.Fatal("expected fofamap skill")
	}
	resolvedBundled, err := filepath.EvalSymlinks(bundled)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Dir != resolvedBundled {
		t.Fatalf("python playbook overwrote MCP skill: dir=%s", meta.Dir)
	}
	content, err := LoadSkillContent(*meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "fofa_account") || strings.Contains(content, "app=\"nginx\"") {
		t.Fatalf("unexpected fofamap skill content:\n%s", content)
	}
}

func TestLoadCatalogCaseInsensitiveOverrideKeepsLastSource(t *testing.T) {
	root := t.TempDir()
	for _, entry := range []struct{ source, dir, name, description string }{
		{"bundled", "audit", "Audit", "bundled version"},
		{"managed", "audit", "audit", "managed version"},
	} {
		dir := filepath.Join(root, entry.source, entry.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		doc := "---\nname: " + entry.name + "\ndescription: " + entry.description + "\n---\n# Body\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := LoadCatalog([]string{filepath.Join(root, "bundled"), filepath.Join(root, "managed")})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Skills) != 1 || catalog.Skills[0].Description != "managed version" {
		t.Fatalf("same-name Skill should be overridden case-insensitively: %#v", catalog.Skills)
	}
}

func TestLoadCatalogParsesWindowsBOMAndDelimiterInsideDescription(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "windows-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "\ufeff---\r\nname: windows-skill\r\ndescription: 'audit --- recover'\r\n---\r\n# Body\r\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	meta, ok := catalog.FindSkill("windows-skill")
	if !ok || meta.Description != "audit --- recover" {
		t.Fatalf("Windows frontmatter was not parsed: %#v", catalog.Skills)
	}
}
