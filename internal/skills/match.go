package skills

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	autoLoadMinScore     = 45
	autoLoadDescOnlyMin  = 70
	autoLoadDefaultLimit = 2
)

// ScoredSkill is a catalog entry with a lexical match score against a user task.
type ScoredSkill struct {
	Meta  SkillMeta
	Score int
}

var skillSearchAliases = map[string][]string{
	"bilibili-play": {"打开b站", "看b站", "b站播放", "播放b站", "哔哩哔哩播放", "倍速", "全屏"},
	"zipcracker": {
		"伪加密", "zip密码", "压缩包密码", "压缩包打不开", "解开zip", "解密zip",
		"zip打不开", "crc32", "zipcrypto", "zipcracker", "zip爆破", "掩码攻击",
		"zip口令", "压缩包口令",
	},
	"fofamap": {"fofa", "测绘", "暴露面", "公网资产", "icon_hash", "fofamap"},
}

var skillPreferredTools = map[string][]string{
	"zipcracker": {"zip_password_recover"},
}

// PreferredTools returns built-ins that should stay in the native schema once
// a Skill has been loaded for the current task.
func PreferredTools(name string) []string {
	return append([]string(nil), skillPreferredTools[strings.ToLower(strings.TrimSpace(name))]...)
}

// Match ranks implicit-invocation Skills against the latest user task.
// limit<=0 uses autoLoadDefaultLimit.
func (c *SkillCatalog) Match(query string, limit int) []ScoredSkill {
	if c == nil || strings.TrimSpace(query) == "" {
		return nil
	}
	if limit <= 0 {
		limit = autoLoadDefaultLimit
	}
	query = strings.ToLower(strings.TrimSpace(query))
	scored := make([]ScoredSkill, 0, len(c.Skills))
	for _, meta := range c.Skills {
		if !meta.AllowImplicit {
			continue
		}
		score, strong := scoreSkill(query, meta)
		if score < autoLoadMinScore {
			continue
		}
		if !strong && score < autoLoadDescOnlyMin {
			continue
		}
		scored = append(scored, ScoredSkill{Meta: meta, Score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return strings.ToLower(scored[i].Meta.Name) < strings.ToLower(scored[j].Meta.Name)
		}
		return scored[i].Score > scored[j].Score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored
}

func scoreSkill(query string, meta SkillMeta) (score int, strong bool) {
	name := strings.ToLower(strings.TrimSpace(meta.Name))
	desc := strings.ToLower(meta.Description)
	if name != "" && strings.Contains(query, name) {
		score += 100
		strong = true
	}
	if strings.EqualFold(name, "zipcracker") && LooksLikeZIPRecover(query) {
		score += 80
		strong = true
	}
	parts := strings.Split(name, "-")
	matchedParts := 0
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len([]rune(part)) >= 3 && strings.Contains(query, part) {
			matchedParts++
		}
	}
	if matchedParts >= 2 || (matchedParts == 1 && len(parts) == 1) {
		score += 35 * matchedParts
		strong = true
	}
	for _, alias := range skillSearchAliases[name] {
		alias = strings.ToLower(strings.TrimSpace(alias))
		if alias == "" || !strings.Contains(query, alias) {
			continue
		}
		strong = true
		runes := []rune(alias)
		if (allHanRunes(runes) && len(runes) >= 2) || len(runes) >= 4 {
			score += 40
		} else {
			score += 24
		}
	}
	descTokens := skillMatchTokenSet(desc + " " + name)
	seen := map[string]bool{}
	for _, token := range skillMatchTokens(query) {
		if !descTokens[token] || seen[token] {
			continue
		}
		seen[token] = true
		score += skillTokenWeight(token)
	}
	return score, strong
}

func skillMatchTokens(text string) []string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := map[string]bool{}
	var tokens []string
	add := func(token string) {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] {
			return
		}
		seen[token] = true
		tokens = append(tokens, token)
	}
	for _, word := range words {
		runes := []rune(word)
		if len(runes) >= 2 {
			add(word)
		}
		var han []rune
		var latin []rune
		flushHan := func() {
			if len(han) == 0 {
				return
			}
			if len(han) <= 4 {
				add(string(han))
			}
			for size := 2; size <= 3; size++ {
				for start := 0; start+size <= len(han); start++ {
					add(string(han[start : start+size]))
				}
			}
			han = han[:0]
		}
		flushLatin := func() {
			if len(latin) >= 3 {
				add(string(latin))
			}
			latin = latin[:0]
		}
		for i, r := range runes {
			switch {
			case unicode.Is(unicode.Han, r):
				flushLatin()
				if i > 0 && unicode.IsLetter(runes[i-1]) && !unicode.Is(unicode.Han, runes[i-1]) {
					add(string(runes[i-1]) + string(r))
				}
				han = append(han, r)
			case unicode.IsLetter(r) || unicode.IsNumber(r):
				flushHan()
				latin = append(latin, r)
			default:
				flushHan()
				flushLatin()
			}
		}
		flushHan()
		flushLatin()
	}
	return tokens
}

func skillMatchTokenSet(text string) map[string]bool {
	out := make(map[string]bool)
	for _, token := range skillMatchTokens(text) {
		out[token] = true
	}
	return out
}

func skillTokenWeight(token string) int {
	runes := []rune(token)
	if allHanRunes(runes) {
		if len(runes) >= 3 {
			return 6
		}
		return 3
	}
	if len(runes) >= 5 {
		return 8
	}
	return 4
}

func allHanRunes(runes []rune) bool {
	if len(runes) == 0 {
		return false
	}
	for _, r := range runes {
		if !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}

var zipPathPattern = regexp.MustCompile(`(?i)(?:[A-Za-z0-9_./\\-]|[\p{Han}])+\.zip`)

var zipRecoverIntentNeedles = []string{
	"解开", "解密", "密码", "口令", "爆破", "伪加密", "打不开",
	"掩码", "crc32", "crack", "password", "recover", "zipcracker",
	"-m", "?d", "?u", "?l", "?s", "?a",
}

// LooksLikeZIPRecover reports whether the user task is ZIP password/pseudo-encryption
// recovery, not a generic download or mention of a .zip asset.
func LooksLikeZIPRecover(query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || !queryMentionsZIPArchive(query) {
		return false
	}
	for _, needle := range zipRecoverIntentNeedles {
		if needle != "" && strings.Contains(query, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func queryMentionsZIPArchive(query string) bool {
	if strings.Contains(query, ".zip") || strings.Contains(query, "zipcracker") ||
		strings.Contains(query, "zipcrypto") || strings.Contains(query, "压缩包") ||
		strings.Contains(query, "zip_password") {
		return true
	}
	for _, token := range skillMatchTokens(query) {
		if token == "zip" || token == "zipcracker" {
			return true
		}
	}
	return false
}

// ExtractZIPSources returns .zip paths mentioned in the user task so the Agent
// can pass them to zip_password_recover without probing cwd first.
func ExtractZIPSources(query string) []string {
	if strings.TrimSpace(query) == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, match := range zipPathPattern.FindAllString(query, 8) {
		match = strings.TrimSpace(match)
		if match == "" || seen[match] {
			continue
		}
		seen[match] = true
		out = append(out, match)
	}
	return out
}
