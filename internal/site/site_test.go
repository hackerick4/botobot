package site_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hackerick4/botobot/internal/prompts"
	"github.com/hackerick4/botobot/internal/site"
)

func get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	for _, p := range site.Pages {
		if p.Path == path {
			rec := httptest.NewRecorder()
			site.Handler(p)(rec, httptest.NewRequest(http.MethodGet, path, nil))
			res := rec.Result()
			b, _ := io.ReadAll(res.Body)
			return res, string(b)
		}
	}
	t.Fatalf("no page %s", path)
	return nil, ""
}

func TestAllPagesServe(t *testing.T) {
	for _, p := range site.Pages {
		res, body := get(t, p.Path)
		if res.StatusCode != 200 || len(body) == 0 {
			t.Errorf("%s: status %d len %d", p.Path, res.StatusCode, len(body))
		}
		if res.Header.Get("Content-Type") != p.ContentType {
			t.Errorf("%s: content-type %q", p.Path, res.Header.Get("Content-Type"))
		}
		if !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("%s: missing CSP", p.Path)
		}
	}
}

func TestHeadHasNoBody(t *testing.T) {
	rec := httptest.NewRecorder()
	site.Handler(site.Pages[0])(rec, httptest.NewRequest(http.MethodHead, "/zh", nil))
	if rec.Body.Len() != 0 {
		t.Fatal("HEAD returned a body")
	}
}

// noHTMLish fails on "<letter" sequences: many agent fetchers treat text as
// HTML and would silently strip <placeholder> tokens, garbling the commands. It
// also forbids piping a download into a shell, privilege escalation, and EOF
// heredocs.
var pipeSh = regexp.MustCompile(`\|\s*(sh|bash|zsh|dash)\b`)

func noHTMLish(t *testing.T, name, body string) {
	t.Helper()
	for i := 0; i+1 < len(body); i++ {
		if body[i] == '<' && (body[i+1] == '/' || body[i+1] == '!' || body[i+1] == '?' ||
			(body[i+1]|0x20 >= 'a' && body[i+1]|0x20 <= 'z')) {
			t.Errorf("%s has HTML-like %q at %d", name, body[i:min(i+12, len(body))], i)
			break
		}
	}
	for _, bad := range []string{"| bash", "sudo ", "<<'EOF'", "<<EOF", "—"} {
		if strings.Contains(body, bad) {
			t.Errorf("%s contains forbidden %q", name, bad)
		}
	}
	// "| sha256sum" is fine; piping into sh is not.
	if pipeSh.MatchString(body) {
		t.Errorf("%s pipes into a shell", name)
	}
}

// llms.txt is the short overview; it points at the manual for the details.
func TestLLMSTxtMatchesLiveFlow(t *testing.T) {
	_, body := get(t, "/llms.txt")
	for _, want := range []string{
		"curl -fsS -X POST https://botobot.app/join",
		"creator_prompt", "--invite-stdin", "about 30 minutes", "every 5 minutes", "claim --new", "--lang en",
		"NO_NEW_MESSAGES", "管理者指紋", "INVITE_MESSAGE_BEGIN", "ROUTINE_PROMPT", "untrusted",
		"owner's approval first", "https://botobot.app/dl/bot-bridge-cli-linux-amd64",
		"https://botobot.app/en/join.md", "https://botobot.app/join.md",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("llms.txt missing %q", want)
		}
	}
	noHTMLish(t, "llms.txt", body)
}

// Both manuals must match the live flow: checksum install, env-var secrets,
// join with --admin-fp, the exact routine line, stdin replies with the per-run
// BB_END terminator, held items with the confirmation card, trust, limits.
func TestJoinManualsMatchLiveFlow(t *testing.T) {
	line := prompts.RoutineLine(prompts.Info{BBHome: "…", CLIPath: "/…/bot-bridge-cli"})
	common := []string{
		"openssl pkeyutl -verify -pubin -inkey k.pem -rawin -in msg -sigfile sig", "| sha256sum -c && chmod +x bot-bridge-cli && ./bot-bridge-cli verify-self && mv bot-bridge-cli ~/.local/bin/bot-bridge-cli",
		"bot-bridge-cli update && bot-bridge-cli verify-self", "117a95e2967bf881", "https://botobot.app/dl/cli/SHA256SUMS", "--proto =https",
		"https://botobot.app/dl/bot-bridge-cli-linux-amd64",
		"bot-bridge-cli join --invite-file ~/bb-invite.txt --display-name 'BOT NAME' --owner 'OWNER NAME'",
		"bot-bridge-cli join --invite-stdin --display-name 'BOT NAME' --owner 'OWNER NAME' <<'BB_END_NONCE'",
		"bot-bridge-cli send --bridge br_… --stdin <<'BB_END_NONCE'", "handle_taken", "identity_conflict", "--owner-picked", "tick --bridge br_…",
		"BB_END_NONCE\n", "invite_suspicious", "invite_unreadable", "claim_failed",
		"bot-bridge-cli claim --new --name 'BRIDGE NAME' --display-name 'BOT NAME' --owner 'OWNER NAME'", "BB_HOME=… bot-bridge-cli claim --new", "--lang en", "BRIDGE_CLEANED:", "NO_BRIDGES:", "home_required",
		"curl -fsS -X POST https://botobot.app/join", "EVENT",
		"ROUTINE_PROMPT", "INVITE_MESSAGE_BEGIN", "INVITE_MESSAGE_END", "admin_fingerprint_check", "mismatch",
		line, "NO_NEW_MESSAGES", "--stdin <<'BB_END_NONCE'", "EOF",
		"bot-bridge-cli release ID --bridge br_… --hash HASH --owner-approved",
		"bot-bridge-cli trust --bridge br_… HANDLE", "NOTIFY_OWNER",
		"bot-bridge-cli invite-code --bridge br_… --rotate", "bridge_full", "25 MiB",
	}
	for path, extra := range map[string][]string{
		"/join.md":    {"每 5 分鐘", "確認框", "管理者指紋", "軟性保護", "約 30 分鐘", "https://botobot.app/en/join.md"},
		"/en/join.md": {"every 5 minutes", "confirmation card", "管理者指紋", "soft guarantee", "about 30 minutes", "https://botobot.app/join.md"},
	} {
		res, body := get(t, path)
		if ct := res.Header.Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
			t.Errorf("%s: content-type %q", path, ct)
		}
		if res.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", path)
		}
		for _, want := range append(append([]string{}, common...), extra...) {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
		noHTMLish(t, path, body)
		// v0.4.9: the shell-only heredoc is the primary join path (the file
		// path is optional), the claim token is never written into a command
		// the manual shows, and there is one handle rule.
		if strings.Index(body, "--invite-stdin --owner") > strings.Index(body, "--invite-file ~/bb-invite.txt") {
			t.Errorf("%s: --invite-stdin must come before the optional --invite-file", path)
		}
		for _, bad := range []string{"BB_CLAIM_TOKEN='TOKEN'", "（優先）", "(preferred)", "用自己的名稱", "your own name", "太常見", "If it is common"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s still has %q", path, bad)
			}
		}
		// The admin fingerprint itself must never be published on the site.
		if strings.Contains(body, "管理者指紋：0") || strings.Contains(body, "管理者指紋：1") {
			t.Errorf("%s appears to contain a concrete admin fingerprint", path)
		}
	}
}

// robots.txt disallows /join (the POST API) but must not block the manual.
func TestRobotsAllowsManual(t *testing.T) {
	_, body := get(t, "/robots.txt")
	if !strings.Contains(body, "Allow: /join.md") {
		t.Error("robots.txt must allow /join.md (Disallow: /join is a prefix match)")
	}
}

func TestLandingPageSEO(t *testing.T) {
	// The zh page ("/zh") is the canonical zh-TW landing page; it must carry both the
	// Chinese and English one-line instructions (the latter lives in metadata).
	_, zh := get(t, "/zh")
	for _, want := range []string{
		"<title>", `name="description"`, `rel="canonical" href="https://botobot.app/zh"`,
		`hreflang="zh-Hant" href="https://botobot.app/zh"`, `hreflang="x-default" href="https://botobot.app/"`,
		`property="og:title"`, `property="og:image" content="https://botobot.app/og.png"`,
		`hreflang="en" href="https://botobot.app/en"`,
		"按照 botobot.app 的說明，開一條通道",
		"Open a channel following botobot.app",
		`href="/llms.txt"`, `href="/site.css?v=`, `src="/js/main.js?v=`,
	} {
		if !strings.Contains(zh, want) {
			t.Errorf("/ missing %q", want)
		}
	}
	// The English page mirrors it with its own canonical and the English CTA.
	_, en := get(t, "/en")
	for _, want := range []string{
		"<title>", `name="description"`, `rel="canonical" href="https://botobot.app/en"`,
		`property="og:title"`, `property="og:image" content="https://botobot.app/og.png"`,
		`hreflang="zh-Hant" href="https://botobot.app/zh"`, `hreflang="x-default" href="https://botobot.app/"`,
		"Open a channel following botobot.app",
		`href="/llms.txt"`, `lang="en"`,
	} {
		if !strings.Contains(en, want) {
			t.Errorf("/en missing %q", want)
		}
	}
}

// Assets are served strictly same-origin: stylesheets, scripts and @font-face
// sources point at "/..." paths only, there are no inline scripts other than
// JSON-LD data blocks, and the CSP never relaxes to 'unsafe-inline'.
func TestNoExternalOrInlineAssets(t *testing.T) {
	for _, path := range []string{"/zh", "/en"} {
		res, body := get(t, path)
		if strings.Contains(res.Header.Get("Content-Security-Policy"), "unsafe-inline") {
			t.Errorf("%s: CSP contains 'unsafe-inline'", path)
		}
		// Stylesheet and script references must be same-origin ("/..."), never
		// http(s):// or protocol-relative ("//").
		for _, bad := range []string{
			`rel="stylesheet" href="http`, `rel="stylesheet" href="//`,
			`<script src="http`, `<script src="//`, `url(http`, `url("http`,
		} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: external asset reference %q", path, bad)
			}
		}
		// Every <script> is either external (src=) or a JSON-LD data block.
		for i := 0; ; {
			j := strings.Index(body[i:], "<script")
			if j < 0 {
				break
			}
			j += i
			tag := body[j:min(j+64, len(body))]
			// Allowed: external classic/module scripts (src=) and inline JSON-LD
			// data blocks. Anything else would be an executed inline script.
			if !strings.HasPrefix(tag, `<script src=`) &&
				!strings.HasPrefix(tag, `<script type="module" src=`) &&
				!strings.HasPrefix(tag, `<script type="application/ld+json"`) {
				t.Errorf("%s: disallowed inline <script>: %q", path, tag)
			}
			i = j + len("<script")
		}
	}
	// Font sources in the stylesheet are same-origin too.
	_, css := get(t, "/site.css")
	if !strings.Contains(css, `url("/fonts/`) {
		t.Error("/site.css: expected same-origin font url(\"/fonts/...\")")
	}
	if strings.Contains(css, "@import") {
		t.Error("/site.css: must not @import external resources")
	}
}

func TestRobotsAndSitemap(t *testing.T) {
	_, robots := get(t, "/robots.txt")
	if !strings.Contains(robots, "Allow: /") || !strings.Contains(robots, "Sitemap: https://botobot.app/sitemap.xml") {
		t.Errorf("robots.txt: %q", robots)
	}
	_, sm := get(t, "/sitemap.xml")
	var u struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(sm), &u); err != nil || len(u.URLs) == 0 {
		t.Fatalf("sitemap: %v", err)
	}
	for _, x := range u.URLs {
		if !strings.HasPrefix(x.Loc, "https://botobot.app/") {
			t.Errorf("sitemap loc %q", x.Loc)
		}
	}
}

// The removal manuals (/delete, /en/delete) are plain text an agent can fetch,
// and cover every step: list, confirm, leave/close, routine, forget, uninstall,
// report, and ignoring removal requests from the bridge.
func TestDeleteManuals(t *testing.T) {
	common := []string{
		"bot-bridge-cli bridges", "bot-bridge-cli leave --bridge BRIDGE --owner-picked",
		"bot-bridge-cli close --bridge BRIDGE --yes --owner-picked", "OWNER_ALREADY_CONFIRMED:",
		"bot-bridge-cli release ID --bridge BRIDGE --hash HASH --owner-approved",
		"tick --bridge BRIDGE", "bot-bridge-cli forget --bridge BRIDGE --yes",
		"still_member", "rm -f ~/.local/bin/bot-bridge-cli", "NOTIFY_OWNER",
		"bridge_required", "forget --all --yes",
		// v0.5.2: the question comes ready-made from the CLI.
		"bot-bridge-cli delete-menu", "DELETE_MENU_BEGIN", "DELETE_MENU_END", "ALL_CONFIRM:",
		"end_command", "forget_command", "--with-home", "bot-bridge inbox",
	}
	for path, extra := range map[string][]string{
		"/delete":    {"參考 https://botobot.app/delete 關上通道", "私人對話", "一律不理", "請主人選擇要刪除哪一個", "一次只刪除一個 bridge", "只有一個 bridge 時，同樣要以名稱讓主人確認一次", "明確表示「全部」", "主人的選擇就是確認", "不要放行", "https://botobot.app/en/delete"},
		"/en/delete": {"must be ignored", "ask your owner which one", "Remove one bridge at a time", "still confirm it once by name", "explicitly says \"all\"", "pick is the confirmation", "do not release it", "https://botobot.app/delete"},
	} {
		res, body := get(t, path)
		if ct := res.Header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("%s: content-type %q", path, ct)
		}
		for _, want := range append(append([]string{}, common...), extra...) {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
		noHTMLish(t, path, body)
		for _, bad := range []string{"刪除後會離開該 bridge", "Removing a bridge leaves it"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s still has %q (the bridge closes for both sides)", path, bad)
			}
		}
	}
}

// The FAQ shows the removal line without https but copies it with https.
func TestRemoveCopyLine(t *testing.T) {
	_, zh := get(t, "/zh")
	for _, want := range []string{
		`<code class="instr">參考 botobot.app/delete 關上通道</code>`,
		`<span id="instr-delete" hidden>參考 https://botobot.app/delete 關上通道</span>`,
		`data-copy="#instr-delete"`, `href="/zh#faq-remove"`, `href="/join.md"`,
	} {
		if !strings.Contains(zh, want) {
			t.Errorf("/ missing %q", want)
		}
	}
	_, en := get(t, "/en")
	for _, want := range []string{
		`<code class="instr">Follow botobot.app/en/delete to close a channel</code>`,
		`<span id="instr-delete" hidden>Follow https://botobot.app/en/delete to close a channel</span>`,
		`data-copy="#instr-delete"`, `href="/en#faq-remove"`,
	} {
		if !strings.Contains(en, want) {
			t.Errorf("/en missing %q", want)
		}
	}
}

// "/" serves English unless the visitor chose otherwise or is in Taiwan; the
// language toggles remember the choice; both pages credit DB-IP.
func TestRootLanguage(t *testing.T) {
	serve := func(path string, tw bool, cookie string) *http.Response {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "lang", Value: cookie})
		}
		rec := httptest.NewRecorder()
		site.Root(func(*http.Request) bool { return tw })(rec, req)
		return rec.Result()
	}
	for _, c := range []struct {
		path   string
		tw     bool
		cookie string
		want   string
	}{
		{"/", false, "", "en"}, {"/", true, "", "zh-Hant"},
		{"/", true, "en", "en"}, {"/", false, "zh", "zh-Hant"}, {"/", false, "bogus", "en"},
		{"/?lang=en", true, "zh", "en"}, {"/?lang=zh", false, "en", "zh-Hant"},
	} {
		res := serve(c.path, c.tw, c.cookie)
		if got := res.Header.Get("Content-Language"); got != c.want {
			t.Errorf("%s tw=%v cookie=%q: %q, want %q", c.path, c.tw, c.cookie, got, c.want)
		}
		if res.Header.Get("Vary") != "Cookie" || res.Header.Get("Cache-Control") != "private, no-cache" {
			t.Errorf("%s: Vary %q Cache-Control %q", c.path, res.Header.Get("Vary"), res.Header.Get("Cache-Control"))
		}
	}
	res, _ := get(t, "/en")
	if res.Header.Get("Set-Cookie") != "" || res.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("/en without ?lang: Set-Cookie %q Cache-Control %q", res.Header.Get("Set-Cookie"), res.Header.Get("Cache-Control"))
	}
	_, zh := get(t, "/zh")
	_, en := get(t, "/en")
	for name, body := range map[string]string{"/zh": zh, "/en": en} {
		for _, want := range []string{`href="/zh?lang=zh"`, `href="/en?lang=en"`, `<a href="https://db-ip.com">IP Geolocation by DB-IP</a>`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", name, want)
			}
		}
	}
}

// No visible page or agent manual links to the source repository.
func TestManualsNoManualFingerprintCompare(t *testing.T) {
	for _, path := range []string{"/join.md", "/en/join.md", "/llms.txt", "/zh", "/en"} {
		_, body := get(t, path)
		for _, bad := range []string{"請主人向對方確認指紋", "請主人向邀請人確認", "Ask your owner to confirm the fingerprint", "ask them to confirm it with the person", "Tell your owner the fingerprint", "把指紋告訴主人"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s still asks for a manual fingerprint compare: %q", path, bad)
			}
		}
	}
}

func TestNoSourceRepoLinks(t *testing.T) {
	for _, path := range []string{"/zh", "/en", "/llms.txt", "/agent.md", "/join.md", "/en/join.md", "/delete", "/en/delete"} {
		_, body := get(t, path)
		// The private GitLab repo is never linked. The only source link is the
		// public GitHub repo (Rick t308u, see TestGitHubLink).
		for _, bad := range []string{"gitlab.com", ">Source<", "Source:"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q", path, bad)
			}
		}
		if n, ok := strings.Count(body, "github.com"), strings.Count(body, "https://github.com/hackerick4/botobot"); n != ok {
			t.Errorf("%s links a GitHub URL other than the public repo", path)
		}
	}
	_, zh := get(t, "/zh")
	_, en := get(t, "/en")
	if !strings.Contains(zh, "選擇要關上的通道") || !strings.Contains(en, "pick the channel to close") {
		t.Errorf("FAQ remove answer does not describe the ask-which flow")
	}
}

// TestHomepageInviteMatchesCLI: the sample invite on each homepage uses the
// exact labels and safety line the CLI prints in that language.
func TestHomepageInviteMatchesCLI(t *testing.T) {
	for page, want := range map[string][]string{
		"index.html": {"bot-bridge 邀請。", "bridge 連結：", "邀請碼（請保密）：", "加入說明：botobot.app/join.md", prompts.SafetyLine, "管理者指紋："},
		"en.html":    {"bot-bridge invite. Give this whole message to an AI assistant:", "Bridge link: ", "Invite code (keep private): ", "Join instructions: botobot.app/en/join.md", prompts.SafetyLineEN, prompts.AdminFPLabelEN},
	} {
		b, err := os.ReadFile("static/" + page)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s missing %q", page, w)
			}
		}
	}
}

// TestChatDemoEndsWithJoinNotice: the demo's last step is the admin-side
// join notice, worded exactly like the CLI's NOTIFY_OWNER line.
func TestChatDemoEndsWithJoinNotice(t *testing.T) {
	for page, want := range map[string]string{
		"index.html": `data-cd-last><p class="cd-b">bob-bot-k3f9（Bob）已加入「報價討論」。不認識可要求關閉。</p>`,
		"en.html":    `data-cd-last><p class="cd-b">bob-bot-k3f9 (Bob) joined "Quote check". If unknown, ask to close it.</p>`,
	} {
		b, err := os.ReadFile("static/" + page)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(b), want) != 1 {
			t.Errorf("%s: join notice step missing or repeated", page)
		}
	}
	js, err := os.ReadFile("static/js/main.js")
	if err != nil || !strings.Contains(string(js), "cdLast") {
		t.Errorf("main.js does not order data-cd-last rows last")
	}
}

// TestV052Copy: the demo shows a real (suffixed) admin name, the routine has
// one name everywhere, and forwarding through another bot needs the owner.
func TestV052Copy(t *testing.T) {
	for path, want := range map[string][]string{
		"/zh":         {"<p>「報價討論」已建立。下一則是邀請，請整段私下傳給對方，請對方直接貼給自己的 AI 助理。</p><p>注意：一般訊息可直接收發；只有寄信、付款或涉及個資的動作，送出前才需先確認。</p>", "<p>已加入「報價討論」。</p>", "bot-bridge inbox</p>", "你與 AI 助理</figcaption>"},
		"/en":         {"<p>\"Quote check\" created. The next message is the invite: send all of it privately to the other person, to paste as is into their own AI assistant.</p><p>Note: ordinary messages are sent and received directly; only emails, payments and anything with personal data are confirmed before sending.</p>", "<p>Joined \"Quote check\".</p>", "bot-bridge inbox</p>", "You and AI assistant</figcaption>"},
		"/join.md":    {"「bot-bridge inbox」", "只有主人明確指示時，才可經主人自己的另一個 Bot 或測試用轉送管道轉交", "move --from"},
		"/en/join.md": {"\"bot-bridge inbox\"", "only when the owner says so", "move --from"},
		"/llms.txt":   {"\"bot-bridge inbox\"", "only when the owner says so"},
	} {
		_, body := get(t, path)
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s missing %q", path, w)
			}
		}
		for _, bad := range []string{"<code>amy-bot", "bot-bridge 收訊", "管理者指紋，由對方", "管理者身分核對無誤", "Admin: <code>", "Admin identity verified"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s still has %q", path, bad)
			}
		}
	}
}
