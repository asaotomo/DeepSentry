package tui

func (m *AgentModel) cachedWelcomeBanner(contentW int) string {
	if m.bannerCache != "" && m.bannerCacheW == contentW {
		return m.bannerCache
	}
	m.bannerCache = renderWelcomeBanner(m.startupInfo, contentW) + "\n\n"
	m.bannerCacheW = contentW
	return m.bannerCache
}
