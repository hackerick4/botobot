package cli

import (
	"strings"
	"unicode"

	"github.com/hackerick4/botobot/internal/cli/pinyindata"
)

// secretInArgv refuses a one-time secret given on the command line, where other
// programs on the computer (and shell history) can see it. The value is never
// echoed back.
func secretInArgv(cmd, flagName, env string) *cliError {
	return &cliError{Code: "secret_in_argv", Exit: exitUsage, Message: cmd + ": do not put the secret in " + flagName +
		" (other programs can read the command line). Nothing was sent. Run it again with the value in " + env +
		", e.g. " + env + "='VALUE' bot-bridge-cli " + cmd + " ... (without " + flagName + ")"}
}

// placeholderHandles and placeholderOwners are example values from prompts and
// manuals that a bot might copy instead of using real names.
var placeholderHandles = map[string]bool{
	"my-bot": true, "handle": true, "h": true, "your-bot": true, "bot-name": true, "your-handle": true, "my-handle": true,
}
var placeholderOwners = map[string]bool{
	"你的主人": true, "主人": true, "主人的名字": true, "owner": true, "owner name": true, "your owner": true, "name": true, "owner_name": true,
}

// placeholderBotNames are the example --display-name values of the manuals.
var placeholderBotNames = map[string]bool{
	"bot name": true, "bot_name": true, "your bot name": true, "此 bot 的名稱": true, "bot 名稱": true, "名稱": true,
}

// checkPlaceholderNames refuses copied example values, so the other side sees
// who this bot really is.
func checkPlaceholderNames(cmd, handle, owner string) *cliError {
	h := strings.ToLower(strings.TrimSpace(handle))
	o := strings.ToLower(strings.TrimSpace(owner))
	if placeholderHandles[h] || strings.ContainsAny(handle, "<>") {
		return &cliError{Code: "placeholder_name", Exit: exitUsage, Message: cmd + ": --handle " + handle +
			" is an example, not a name. Nothing was sent. Use your own name in lowercase letters, digits and hyphens; if it is a common name, use your owner's first name plus -bot (e.g. amy-bot)"}
	}
	if placeholderOwners[o] || strings.ContainsAny(owner, "<>") {
		return &cliError{Code: "placeholder_name", Exit: exitUsage, Message: cmd + ": --owner " + owner +
			" is an example, not a name. Nothing was sent. Use your owner's real name (the name you call them by), or leave --owner out"}
	}
	return nil
}

// slugName turns a name into handle letters: lowercase latin letters and
// digits, with spaces, hyphens, underscores and dots as single hyphens. Since
// v0.5.4 Chinese characters become toneless pinyin (one run of characters is
// one word: "小助手 X" -> "xiaozhushou-x", "王小明" -> "wangxiaoming") and
// full-width latin letters count as latin. Other scripts (kana, hangul, …) are
// dropped; when more letters are dropped than kept, or fewer than two letters
// or digits remain, the name gives "" so the caller falls back instead of
// producing a handle like "a-x7k2" that no one would recognise.
func slugName(name string) string {
	var sb strings.Builder
	kept, dropped := 0, 0
	inHan := false
	for _, r := range name {
		if r >= 0xFF01 && r <= 0xFF5E { // full-width ASCII
			r -= 0xFEE0
		}
		r = unicode.ToLower(r)
		if py := pinyindata.Lookup(r); py != "" {
			if !inHan {
				sb.WriteByte('-')
			}
			sb.WriteString(py)
			kept += len(py)
			inHan = true
			continue
		}
		if inHan {
			sb.WriteByte('-')
			inHan = false
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
			kept++
		case r == ' ' || r == '-' || r == '_' || r == '.':
			sb.WriteByte('-')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			dropped++
			sb.WriteByte('-')
		}
	}
	h := strings.Trim(sb.String(), "-")
	for strings.Contains(h, "--") {
		h = strings.ReplaceAll(h, "--", "-")
	}
	if kept < 2 || dropped > kept {
		return ""
	}
	return h
}

// defaultHandle fills an empty --handle from the owner's name ("Amy Lin" ->
// amy-lin-bot, "王小明" -> wangxiaoming-bot since v0.5.4); a name slugName
// cannot use leaves it empty and the command asks for --handle.
func defaultHandle(handle, owner string) string {
	if handle != "" || owner == "" {
		return handle
	}
	h := slugName(owner)
	if h == "" {
		return ""
	}
	if len(h) > 28 {
		h = strings.TrimRight(h[:28], "-")
	}
	return h + "-bot"
}
