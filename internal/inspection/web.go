package inspection

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"ai-edr/internal/config"
	"github.com/chromedp/chromedp"
)

func validWebURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.Fragment == ""
}
func sameOrigin(a, b string) bool {
	u, e := url.Parse(a)
	v, f := url.Parse(b)
	return e == nil && f == nil && u.Scheme == v.Scheme && u.Host == v.Host
}
func validateWebDevice(d config.InspectionDevice) error {
	if !validWebURL(d.URL) {
		return fmt.Errorf("%s 网页地址无效", d.Name)
	}
	if d.PasswordEnv != "" && (d.UsernameEnv == "" || d.UsernameSelector == "" || d.PasswordSelector == "" || d.SubmitSelector == "" || d.ReadySelector == "") {
		return fmt.Errorf("%s 网页登录需要用户名/密码环境变量及登录、成功标志选择器", d.Name)
	}
	for _, c := range d.Checks {
		if c.URL != "" && (!validWebURL(c.URL) || !sameOrigin(d.URL, c.URL)) {
			return fmt.Errorf("%s/%s 检查页面必须与设备地址同源", d.Name, c.Name)
		}
	}
	return nil
}
func browserPath(configured string) string {
	if configured != "" {
		return configured
	}
	if p := os.Getenv("DEEPSENTRY_BROWSER_BINARY"); p != "" {
		return p
	}
	for _, p := range []string{"chromium", "chromium-browser", "google-chrome", "chrome"} {
		if found, e := exec.LookPath(p); e == nil {
			return found
		}
	}
	if runtime.GOOS == "darwin" {
		p := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, e := os.Stat(p); e == nil {
			return p
		}
	}
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if strings.TrimSpace(base) == "" {
				continue
			}
			p := base + `\Google\Chrome\Application\chrome.exe`
			// #nosec G703 -- Local operator-controlled Windows installation directories; only checks for the browser executable, not a remote request path.
			if _, e := os.Stat(p); e == nil {
				return p
			}
		}
	}
	return ""
}
func newWebCollector(parent context.Context, binary string, d config.InspectionDevice) (Collector, func(), error) {
	noop := func() {}
	path := browserPath(binary)
	if path == "" {
		return nil, noop, errors.New("网页巡检需要本机 Chrome/Chromium（支持 headless，无需桌面）；请配置 browser_binary")
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(path), chromedp.WindowSize(1440, 1000), chromedp.Flag("no-sandbox", false))
	alloc, cancelAlloc := chromedp.NewExecAllocator(parent, opts...)
	browser, cancelBrowser := chromedp.NewContext(alloc)
	cleanup := func() { cancelBrowser(); cancelAlloc() }
	if err := chromedp.Run(browser, chromedp.Navigate(d.URL)); err != nil {
		return nil, cleanup, errors.New("设备网页无法打开；请检查网络、证书和浏览器运行权限")
	}
	var location string
	if err := chromedp.Run(browser, chromedp.Location(&location)); err != nil || !sameOrigin(d.URL, location) {
		return nil, cleanup, errors.New("设备网页跳转到其他域名；请为实际登录地址配置独立配置")
	}
	if d.PasswordEnv != "" {
		user, password := os.Getenv(d.UsernameEnv), os.Getenv(d.PasswordEnv)
		if user == "" || password == "" {
			return nil, cleanup, errors.New("缺少设备登录凭据环境变量")
		}
		err := chromedp.Run(browser, chromedp.WaitVisible(d.UsernameSelector, chromedp.ByQuery), chromedp.SetValue(d.UsernameSelector, user, chromedp.ByQuery), chromedp.SetValue(d.PasswordSelector, password, chromedp.ByQuery), chromedp.Click(d.SubmitSelector, chromedp.ByQuery), chromedp.WaitVisible(d.ReadySelector, chromedp.ByQuery))
		if err != nil {
			return nil, cleanup, errors.New("登录未完成（账号、选择器、验证码或 MFA）；不能据此判断设备正常")
		}
	}
	collect := func(ctx context.Context, _ config.InspectionDevice, c config.InspectionCheck) (string, []byte, error) {
		task, cancel := context.WithCancel(browser)
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		if c.URL != "" {
			if err := chromedp.Run(task, chromedp.Navigate(c.URL)); err != nil {
				return "", nil, errors.New("检查页面打开失败")
			}
		}
		var text, where string
		if err := chromedp.Run(task, chromedp.Location(&where)); err != nil || !sameOrigin(d.URL, where) {
			return "", nil, errors.New("登录已失效或页面发生跨域跳转")
		}
		if d.ReadySelector != "" {
			if err := chromedp.Run(task, chromedp.WaitVisible(d.ReadySelector, chromedp.ByQuery)); err != nil {
				return "", nil, errors.New("无法验证登录状态；页面可能已退出登录")
			}
		}
		if err := chromedp.Run(task, chromedp.WaitVisible(c.Selector, chromedp.ByQuery), chromedp.Text(c.Selector, &text, chromedp.ByQuery)); err != nil {
			return "", nil, errors.New("检查区域不可见或内容未加载")
		}
		var png []byte
		if c.Screenshot {
			if err := chromedp.Run(task, chromedp.Screenshot(c.Selector, &png, chromedp.ByQuery)); err != nil {
				return text, nil, errors.New("截图失败")
			}
		}
		return strings.TrimSpace(text), png, nil
	}
	return collect, cleanup, nil
}
