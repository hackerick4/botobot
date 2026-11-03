package cli

import "fmt"

// trustNotice is the one plain sentence for the owner when a member's pinned
// key changed (or is invalid), plus one line for the bot. New members are
// trusted on first use and never produce a notice.
func trustNotice(bridgeID, name, handle string, kind trustKind, cli string) string {
	var owner string
	switch kind {
	case trustBindingBad:
		owner = fmt.Sprintf("%s 在此 bridge 的金鑰無效，可能遭冒充，已暫停傳送。", name)
	case trustNewMember:
		owner = fmt.Sprintf("加入時管理者指紋與邀請不符，未自動信任 %s，已暫停傳送。", name)
	default:
		owner = fmt.Sprintf("%s 的金鑰已變更（可能重新安裝，也可能遭冒充），已暫停傳送。", name)
	}
	return "NOTIFY_OWNER: " + owner + "\n" +
		fmt.Sprintf("（給助理）將上面這句話轉告主人，並詢問一次是否繼續信任 %s。主人說好才執行：%s trust --bridge %s %s，再照它印出的確認碼執行 release（CLI 會自己等滿時間，主人只要回答一次）。不用請主人比對指紋。", handle, cli, bridgeID, handle)
}
