package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/hackerick4/botobot/internal/prompts"
)

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// menuItem is one bridge in the delete menu.
type menuItem struct {
	sess *session
	bc   *bridgeConfig
	line map[string]any
	// ec is set for the public entry itself (v0.6.1); bc is nil then.
	ec *entryConfig
}

// deleteMenu prints the owner's /delete question ready-made (v0.5.2), so the
// bot does not compose it: every bridge of this bot with one unique number
// (across --with-home folders too), the "delete all" option with its one extra
// confirmation, and "delete nothing". Each option also comes as a JSON line
// with the exact commands for that bridge, prefixed with its BB_HOME.
func (s *session) deleteMenu(offline bool, lang string, withHomes []string, all bool) *cliError {
	sessions := []*session{s}
	seen := map[string]bool{absOr(s.ks.home): true}
	for _, h := range withHomes {
		h = expandTilde(h)
		abs := absOr(h)
		if seen[abs] {
			continue
		}
		if fi, err := os.Lstat(abs); err != nil || !fi.IsDir() {
			return usageErrorf("bridges: --with-home %s is not a folder (links are not followed)", h)
		}
		if !isBotFolder(abs) {
			return usageErrorf("bridges: --with-home %s is not a bot-bridge folder (no config.json or br_* bridge); nothing touched", h)
		}
		seen[abs] = true
		gg := *s.g
		gg.home = abs
		other, ce := s.app.newSession(&gg)
		if ce != nil {
			return ce
		}
		other.pr = s.pr
		sessions = append(sessions, other)
	}
	var items []menuItem
	var entryItems []menuItem
	allEN := true
	for _, ss := range sessions {
		bridges, err := ss.ks.listBridges()
		if err != nil {
			return generalErrorf("list bridges: %v", err)
		}
		for _, bc := range bridges {
			items = append(items, menuItem{sess: ss, bc: bc, line: ss.describeBridge(bc, offline)})
			if !prompts.IsEnglish(bc.Lang) {
				allEN = false
			}
		}
		// v0.6.1: the public entry itself is an item too. It comes after
		// every bridge and line, so "remove all" closes the lines one by one
		// before the entry.
		for _, ec := range ss.ks.listEntries() {
			entryItems = append(entryItems, menuItem{sess: ss, ec: ec})
			if !prompts.IsEnglish(ec.Lang) {
				allEN = false
			}
		}
	}
	en := allEN && (len(items) > 0 || len(entryItems) > 0)
	switch strings.ToLower(lang) {
	case "en":
		en = true
	case "zh", "zh-tw":
		en = false
	case "":
	default:
		return usageErrorf("bridges: --lang must be zh or en")
	}
	// v0.5.4: one creation-time order across all folders.
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].bc, items[j].bc
		if a.JoinedAt != b.JoinedAt {
			return a.JoinedAt < b.JoinedAt
		}
		return a.BridgeID < b.BridgeID
	})
	items = append(items, entryItems...)
	if all {
		return s.deleteAll(items, en)
	}
	hasEntry := len(entryItems) > 0
	for _, it := range items {
		if it.bc != nil && it.bc.EntryID != "" {
			hasEntry = true
		}
	}
	var names []string
	for i, it := range items {
		n := i + 1
		pre := it.sess.cliCmd() + " "
		if it.ec != nil {
			lines := 0
			for _, o := range items {
				if o.bc != nil && o.bc.EntryID == it.ec.EntryID {
					lines++
				}
			}
			s.pr.emit(map[string]any{
				"type": "delete_option", "n": n, "kind": "entry", "entry_id": it.ec.EntryID, "name": sanitizeDisplayName(it.ec.Name),
				"lines": lines, "home": absOr(it.sess.ks.home),
				"end_command":    pre + "entry close --entry " + it.ec.EntryID + " --yes --owner-picked",
				"forget_command": "",
			})
			names = append(names, entryMenuLabel(it.ec, en))
			continue
		}
		bid := it.bc.BridgeID
		status, _ := it.line["status"].(string)
		end := ""
		forget := pre + "forget --bridge " + bid + " --yes"
		if status != "closed" && status != "left" {
			if it.bc.Role == "admin" {
				end = pre + "close --bridge " + bid + " --yes --owner-picked"
			} else {
				end = pre + "leave --bridge " + bid + " --owner-picked"
			}
			// Closing one line of this bot's public entry already removes
			// it here; there is nothing left to forget.
			if it.bc.EntryID != "" {
				forget = ""
			}
		}
		kind := "bridge"
		if it.bc.EntryID != "" {
			kind = "entry_line"
		}
		s.pr.emit(map[string]any{
			"type": "delete_option", "n": n, "kind": kind, "bridge_id": bid, "name": it.line["name"],
			"role": it.bc.Role, "status": status, "home": absOr(it.sess.ks.home),
			"end_command":    end,
			"forget_command": forget,
		})
		names = append(names, menuLabel(it.bc, it.line, en))
	}
	total := len(items)
	out := []string{"DELETE_MENU_BEGIN"}
	unit, unitEN, unitAll, unitCount := "個 bridge", "bridges", "個 bridge ", "個"
	if hasEntry {
		unit, unitEN, unitAll, unitCount = "項", "items", "項", "項"
	}
	switch {
	case total == 0 && en:
		out = append(out, "This bot has no bridge. Nothing to remove.")
	case total == 0:
		out = append(out, "此 Bot 目前沒有 bridge，沒有可刪除的項目。")
	case en:
		q := fmt.Sprintf("%d %s found. Which one should be removed?", total, unitEN)
		if total == 1 {
			q = "Remove the bridge " + names[0] + "?"
			if items[0].ec != nil || items[0].bc.EntryID != "" {
				q = "Close " + names[0] + "?"
			}
		}
		tail := " Removing a bridge closes it for both sides, tells the other side once, and deletes its local keys. This cannot be undone. Other bridges are not affected."
		if hasEntry {
			tail += " Closing one line of the public entry leaves the entry and the other lines open; closing the public entry ends its link and every line."
		}
		out = append(out, q+tail)
	default:
		q := fmt.Sprintf("目前有 %d %s，要刪除哪一個？", total, unit)
		if total == 1 {
			q = "要刪除" + names[0] + "嗎？"
			if items[0].ec != nil || items[0].bc.EntryID != "" {
				q = "要關閉" + names[0] + "嗎？"
			}
		}
		tail := "刪除後 bridge 對雙方關閉，對方收到一次通知，本機金鑰一併移除，無法復原；其他 bridge 不受影響。"
		if hasEntry {
			tail += "只關一條公開入口的連線時，入口和其他連線照常；關閉公開入口會讓連結失效，所有連線一起結束。"
		}
		out = append(out, q+tail)
	}
	for i, it := range items {
		if it.ec != nil {
			out = append(out, fmt.Sprintf("%d. %s", i+1, entryMenuOption(it, items, names[i], en)))
			continue
		}
		out = append(out, fmt.Sprintf("%d. %s", i+1, menuOption(it, names[i], en)))
	}
	if total > 1 {
		if en {
			out = append(out, fmt.Sprintf("%d. Remove all %d (asks once more)", total+1, total))
		} else {
			out = append(out, fmt.Sprintf("%d. 全部刪除（%d %s，會再確認一次）", total+1, total, unitCount))
		}
	}
	if total > 0 {
		k := total + 1
		if total > 1 {
			k = total + 2
		}
		if en {
			out = append(out, fmt.Sprintf("%d. Keep all", k))
		} else {
			out = append(out, fmt.Sprintf("%d. 都不刪除", k))
		}
	}
	out = append(out, "DELETE_MENU_END")
	for _, ln := range out {
		s.pr.raw(ln)
	}
	if total == 0 {
		return s.pr.done()
	}
	if total > 1 {
		// v0.6.2: one command holds every removal, so "remove all" waits once.
		allCmd := s.cliCmd() + " bridges --delete-menu --all --owner-picked"
		for _, ss := range sessions[1:] {
			allCmd += " --with-home " + shellArg(absOr(ss.ks.home))
		}
		if offline {
			allCmd += " --offline"
		}
		if l := strings.ToLower(lang); l == "en" || l == "zh" || l == "zh-tw" {
			allCmd += " --lang " + l
		}
		s.pr.raw("ALL_COMMAND: " + allCmd)
		if en {
			s.pr.raw(fmt.Sprintf("ALL_CONFIRM: Remove all %d %s: %s? This cannot be undone.", total, unitEN, strings.Join(names, ", ")))
		} else {
			s.pr.raw(fmt.Sprintf("ALL_CONFIRM: 這 %d %s全部刪除？%s。刪除後無法復原。", total, unitAll, strings.Join(names, "、")))
		}
	}
	if en {
		s.pr.raw("NEXT: show the owner the text between DELETE_MENU_BEGIN and DELETE_MENU_END as is (without those two lines; a choice card is fine). " +
			"One number: the owner picking it here is the confirmation (no second question or card): run that delete_option's end_command (skip if empty), then the release line it prints, then its forget_command (skip if empty). " +
			"Remove all: ask the ALL_CONFIRM: sentence once as is; on yes run only the ALL_COMMAND: line (not the end_commands one by one), then the one release line it prints (it waits only once, up to about a minute), then the forget lines printed. " +
			"Keep all, or an answer that matches no number: do nothing; ask again or stop.")
		return s.pr.done()
	}
	s.pr.raw("NEXT: 把 DELETE_MENU_BEGIN 到 DELETE_MENU_END 之間的文字原樣給主人選（不含這兩行；可做成選項卡），不要改寫。" +
		"主人選一個編號：這次選擇就是確認，不要再問、不要再跳確認框；先執行該編號 delete_option 的 end_command（空白就略過），再執行它印出的 release 那一行，最後執行 forget_command（空白就略過）。" +
		"選「全部刪除」：原樣問 ALL_CONFIRM: 後那一句一次；同意後只執行 ALL_COMMAND: 後那一行（不要逐項執行 end_command），再執行它印出的那一行 release（只需等一次，最多約 1 分鐘），最後照印出的 forget 行執行。" +
		"選「都不刪除」或回答對應不到編號：不動作，再問一次或結束。")
	return s.pr.done()
}

// entryMenuLabel names the public entry itself in the menu (v0.6.1).
func entryMenuLabel(ec *entryConfig, en bool) string {
	if en {
		return "the public entry \"" + sanitizeDisplayName(ec.Name) + "\""
	}
	return "公開入口「" + sanitizeDisplayName(ec.Name) + "」"
}

// entryMenuOption is the numbered line for the public entry itself.
func entryMenuOption(it menuItem, items []menuItem, label string, en bool) string {
	var guests []string
	unread := 0
	for _, o := range items {
		if o.bc != nil && o.bc.EntryID == it.ec.EntryID {
			guests = append(guests, sanitizeDisplayName(o.bc.Peer))
			unread += undeliveredOf(o.line)
		}
	}
	if en {
		who := "no line is open"
		if len(guests) > 0 {
			who = fmt.Sprintf("%d lines end with it: %s", len(guests), strings.Join(guests, ", "))
		}
		if unread > 0 {
			who += "; " + unreadEN(unread)
		}
		return "Close " + label + " (the link stops working; " + who + ")"
	}
	who := "目前沒有連線"
	if len(guests) > 0 {
		who = fmt.Sprintf("%d 條連線一起結束：%s", len(guests), strings.Join(guests, "、"))
	}
	if unread > 0 {
		who += "，" + unreadZh(unread)
	}
	return "關閉" + label + "（連結失效，" + who + "）"
}

// menuLabel is 「name」 (or the bridge id) for the menu; one line of this
// bot's public entry is named by the joining bot (v0.6.1).
func menuLabel(bc *bridgeConfig, line map[string]any, en bool) string {
	if bc.EntryID != "" {
		g := sanitizeDisplayName(bc.Peer)
		if en {
			return "the line with " + g + " (public entry \"" + sanitizeDisplayName(bc.Name) + "\")"
		}
		return "與 " + g + " 的連線（公開入口「" + sanitizeDisplayName(bc.Name) + "」）"
	}
	name, _ := line["name"].(string)
	name = sanitizeDisplayName(name)
	if name == "?" || name == "" {
		name = bc.BridgeID
	}
	if en {
		return "\"" + name + "\""
	}
	return "「" + name + "」"
}

// menuOption is one numbered line: what goes, with whom, and when.
func menuOption(it menuItem, label string, en bool) string {
	l := it.line
	status, _ := l["status"].(string)
	peers, _ := l["peers"].([]string)
	left, _ := l["peer_left"].(bool)
	joined, _ := l["joined_at"].(string)
	peerJoined, _ := l["peer_joined_at"].(string)
	last, _ := l["last_activity"].(string)
	admin := it.bc.Role == "admin"
	var parts []string
	if it.bc.EntryID != "" {
		// v0.6.1: one line of this bot's public entry.
		if peerJoined == "" {
			peerJoined = joined
		}
		if en {
			if peerJoined != "" {
				parts = append(parts, "joined "+peerJoined)
			}
			if last != "" {
				parts = append(parts, "last activity "+last)
			}
			parts = append(parts, "the entry and the other lines stay open")
			if n := undeliveredOf(l); n > 0 {
				parts = append(parts, unreadEN(n))
			}
			return "Close " + label + " (" + strings.Join(parts, "; ") + ")"
		}
		if peerJoined != "" {
			parts = append(parts, "加入："+peerJoined)
		}
		if last != "" {
			parts = append(parts, "最近活動："+last)
		}
		parts = append(parts, "入口和其他連線照常")
		if n := undeliveredOf(l); n > 0 {
			parts = append(parts, unreadZh(n))
		}
		return "關閉" + label + "（" + strings.Join(parts, "，") + "）"
	}
	if en {
		switch {
		case len(peers) > 0:
			ps := strings.NewReplacer("（", " (", "）", ")").Replace(strings.Join(peers, ", "))
			parts = append(parts, "with "+ps)
		case left:
			parts = append(parts, "the other side left")
		default:
			parts = append(parts, "no one has joined yet")
		}
		if it.bc.Role == "admin" {
			parts = append(parts, "created by this bot")
		} else {
			parts = append(parts, "this bot was invited")
		}
		if joined != "" && admin {
			parts = append(parts, "created "+joined)
			if peerJoined != "" {
				parts = append(parts, "the other side joined "+peerJoined)
			}
		} else if joined != "" {
			parts = append(parts, "joined "+joined)
		}
		if last != "" {
			parts = append(parts, "last activity "+last)
		} else {
			parts = append(parts, "no messages in 48 hours")
		}
		switch status {
		case "closed", "left":
			parts = append(parts, "already closed, only local data is deleted")
		case "unknown":
			parts = append(parts, "status not confirmed right now")
		}
		if n := undeliveredOf(l); n > 0 {
			parts = append(parts, unreadEN(n))
		}
		return "Remove " + label + " (" + strings.Join(parts, "; ") + ")"
	}
	switch {
	case len(peers) > 0:
		parts = append(parts, "對象："+strings.Join(peers, "、"))
	case left:
		parts = append(parts, "對方已離開")
	default:
		parts = append(parts, "對方尚未加入")
	}
	if it.bc.Role == "admin" {
		parts = append(parts, "此 Bot 建立")
	} else {
		parts = append(parts, "此 Bot 受邀加入")
	}
	if joined != "" && admin {
		parts = append(parts, "建立："+joined)
		if peerJoined != "" {
			parts = append(parts, "對方加入："+peerJoined)
		}
	} else if joined != "" {
		parts = append(parts, "加入："+joined)
	}
	if last != "" {
		parts = append(parts, "最近活動："+last)
	} else {
		parts = append(parts, "48 小時內無訊息")
	}
	switch status {
	case "closed", "left":
		parts = append(parts, "已關閉，只刪本機資料")
	case "unknown":
		parts = append(parts, "目前無法確認狀態")
	}
	if n := undeliveredOf(l); n > 0 {
		parts = append(parts, unreadZh(n))
	}
	return "刪除" + label + "（" + strings.Join(parts, "，") + "）"
}

// expandTilde turns a leading ~/ into the user's home directory.
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// deleteAll is `bridges --delete-menu --all --owner-picked` (v0.6.2): the
// owner chose "remove all" and answered ALL_CONFIRM. Every removal is held at
// once (same held items, codes and 30s delay as one by one), then one release
// line per folder releases them all after a single wait. If any item cannot
// be held, the ones already held are dropped and nothing changes.
func (s *session) deleteAll(items []menuItem, en bool) *cliError {
	a := s.app
	var held []*heldItem
	a.holdSink = &held
	defer func() { a.holdSink = nil }()
	homes := map[string]string{} // held id -> home
	var order []string           // homes in first-use order
	var forgets []string
	for _, it := range items {
		home := absOr(it.sess.ks.home)
		var argv []string
		switch {
		case it.ec != nil:
			argv = []string{"entry", "close", "--home", home, "--entry", it.ec.EntryID, "--yes", "--owner-picked"}
		default:
			bid := it.bc.BridgeID
			status, _ := it.line["status"].(string)
			if status == "closed" || status == "left" {
				forgets = append(forgets, it.sess.cliCmd()+" forget --bridge "+bid+" --yes")
				continue
			}
			if it.bc.Role == "admin" {
				argv = []string{"close", "--home", home, "--bridge", bid, "--yes", "--owner-picked"}
			} else {
				argv = []string{"leave", "--home", home, "--bridge", bid, "--owner-picked"}
			}
		}
		before := len(held)
		fn := commands[argv[0]]
		if pred, gated := adminGated[argv[0]]; gated {
			fn = gateAdmin(argv[0], fn, pred)
		}
		ce := requireExplicitBridge(argv[0], argv[1:])
		if ce == nil {
			ce = fn(a, argv[1:])
		}
		if ce == nil && len(held) != before+1 {
			ce = generalErrorf("delete-menu --all: %s was not held", argv[0])
		}
		if ce != nil {
			for _, h := range held {
				_ = os.Remove(filepath.Join(homes[h.ID], "held", h.ID+".json"))
			}
			held = nil
			return ce
		}
		h := held[len(held)-1]
		homes[h.ID] = home
		if !slices.Contains(order, home) {
			order = append(order, home)
		}
	}
	a.holdSink = nil
	var ids []map[string]any
	for _, h := range held {
		ids = append(ids, map[string]any{"id": h.ID, "hash": shortHash(h.Hash), "reasons": h.Reasons})
	}
	s.pr.emit(map[string]any{"type": "held_all", "count": len(held), "items": ids})
	if len(held) == 0 {
		if en {
			s.pr.raw("NEXT: nothing is open to close; run every non-empty forget_command.")
		} else {
			s.pr.raw("NEXT: 沒有要關閉的項目；執行每個不是空白的 forget_command。")
		}
		return s.pr.done()
	}
	if en {
		s.pr.raw("OWNER_ALREADY_CONFIRMED: the owner chose \"remove all\" in your private chat and answered ALL_CONFIRM; that is the confirmation. Do not ask again or show a card; run the release line below now. It waits only once, up to about a minute, for all of them.")
	} else {
		s.pr.raw("OWNER_ALREADY_CONFIRMED: 主人已在私人對話中選了「全部刪除」並回答了 ALL_CONFIRM，這就是確認。不要再問主人，也不要顯示確認框，直接執行下面的 release 行；只需等一次，最多約 1 分鐘。")
	}
	for _, home := range order {
		var pairs []string
		var cli string
		for _, h := range held {
			if homes[h.ID] == home {
				pairs = append(pairs, h.ID+":"+shortHash(h.Hash))
			}
		}
		for _, it := range items {
			if absOr(it.sess.ks.home) == home {
				cli = it.sess.cliCmd()
				break
			}
		}
		s.pr.raw("NEXT: " + cli + " release --batch " + strings.Join(pairs, ",") + " --owner-approved")
	}
	for _, f := range forgets {
		s.pr.raw("NEXT: " + f)
	}
	if en {
		s.pr.raw("Then run each forget line the release prints. Afterwards just tell the owner briefly that everything chosen was removed.")
	} else {
		s.pr.raw("之後照 release 印出的 forget 行執行。完成後只需簡短告知主人：選定的項目都已刪除。")
	}
	return s.pr.done()
}

// shellArg quotes p for a printed command line when needed.
func shellArg(p string) string {
	if shellSafe.MatchString(p) {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// undeliveredOf reads describeBridge's "undelivered" count (v0.6.4).
func undeliveredOf(line map[string]any) int {
	n, _ := line["undelivered"].(int)
	return n
}

// unreadZh / unreadEN say how many of this bot's messages the other side has
// not read yet (v0.6.4), for the delete menu and status.
func unreadZh(n int) string { return fmt.Sprintf("還有 %d 則對方尚未讀取", n) }

func unreadEN(n int) string {
	if n == 1 {
		return "1 message not yet read by the other side"
	}
	return fmt.Sprintf("%d messages not yet read by the other side", n)
}
