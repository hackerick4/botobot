package judge

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Category is an inbound threat category (EXFIL_DEFENSE.md §4.4).
type Category string

// Inbound categories.
const (
	ISecret    Category = "I-SECRET"    // asks for keys, passwords, tokens, local files
	IOwnerData Category = "I-OWNERDATA" // asks for the owner's private data
	IInject    Category = "I-INJECT"    // tries to override instructions / forge control text
	IAuth      Category = "I-AUTH"      // claims owner/admin/official approval or identity
	IExec      Category = "I-EXEC"      // asks to run commands or bot-bridge admin actions
	IExfilURL  Category = "I-EXFILURL"  // asks to send data to a URL / webhook / image link
	IEnc       Category = "I-ENC"       // asks to encode, reverse or spell out (filter evasion)
	IOracle    Category = "I-ORACLE"    // yes/no or partial-reveal probing (salami)
	IObf       Category = "I-OBF"       // obfuscated text (invisible chars, homoglyphs, spacing)
	IUrgent    Category = "I-URGENT"    // pressure / urgency / threats
)

// Level is a coarse risk level.
type Level int

// Levels, ordered.
const (
	LevelNone Level = iota
	LevelLow
	LevelMedium
	LevelHigh
)

func (l Level) String() string {
	return [...]string{"none", "low", "medium", "high"}[l]
}

// Hit is one rule match. It never carries the matched text.
type Hit struct {
	Rule     string   `json:"rule"`
	Category Category `json:"category"`
	Weight   int      `json:"weight"`
}

// InboundResult is the judge's verdict for one inbound message.
type InboundResult struct {
	Level      Level
	Score      int
	Categories []Category
	Hits       []Hit
}

// Taints reports whether this message should open the bridge's taint window:
// any manipulation attempt judged medium or above (urgency/obfuscation alone
// never taint).
func (r InboundResult) Taints() bool {
	if r.Level < LevelMedium {
		return false
	}
	for _, c := range r.Categories {
		if c != IUrgent && c != IObf {
			return true
		}
	}
	return false
}

// Shared vocabulary (folded: lower-case, traditional Chinese, ASCII punctuation).
const (
	secretNounEN = `(?:api[ _-]?keys?|apikeys?|private[ _-]?keys?|secret[ _-]?keys?|signing[ _-]?keys?|keys?[ _-]?files?|secrets?|passwords?|passwd|passphrases?|pass ?codes?|pin(?: codes?| numbers?)?|access[ _-]?tokens?|auth(?:entication)?[ _-]?tokens?|bearer[ _-]?tokens?|refresh[ _-]?tokens?|session[ _-]?(?:tokens?|cookies?|ids?)|tokens?|credentials?|creds|ssh[ _-]?keys?|gpg[ _-]?keys?|seed[ _-]?phrases?|mnemonics?|recovery[ _-]?(?:phrases?|codes?|keys?)|backup[ _-]?codes?|2fa[ _-]?codes?|otp|one[ _-]?time[ _-]?(?:codes?|passwords?)|verification[ _-]?codes?|login[ _-]?codes?|cookies?|\.env|env(?:ironment)?[ _-]?var(?:iable)?s?|claim[ _-]?tokens?|bbk_|bbe_|keys\.json|wallet[ _-]?keys?)`
	secretNounZH = `(?:金鑰|密鑰|私鑰|祕鑰|秘鑰|密碼|口令|通行碼|通關密語|驗證碼|動態碼|一次性密碼|簡訊碼|令牌|憑證|憑據|api ?key|助記詞|恢復碼|備援碼|備用碼|環境變數|設定檔|配置文件|配置檔|cookie|session|認領碼|pin碼|金鑰檔|私鑰檔|錢包)`
	verbEN       = `(?:send|give|share|show|tell|reveal|print|paste|post|forward|provide|output|dump|display|list|read|cat|copy|type|spell|leak|export|upload|e-?mail|dm|text|disclose|expose|include|attach|echo|return|reply with|respond with|write (?:out|down)|hand over|pass along|read out|let me (?:see|have|know)|what(?:'s| is| are)|need|want|get|collect|gather|drop)`
	verbZH       = `(?:給|傳|發|送|寄|告訴|告知|提供|貼|分享|顯示|輸出|列出|印出|讀出|唸|念|回覆|回傳|複製|上傳|透露|洩漏|洩露|公開|附上|附加|交出|報給|說出|講出|打出|填|看一下|看看|是什麼|是多少|多少|拿來|發過來|傳過來|告訴我|貼上|貼過來)`
	possEN       = `(?:` + youEN + `|[a-z][a-z.-]{1,20}'[s5])`
	youEN        = `(?:your|ur|yours|you(?:r)? own|the owner'?s|owner'?s|your (?:owner|boss|user|human)'?s|his|her|their)`
	youZH        = `(?:你的|妳的|您的|你們的|你們|你家|你主人的?|主人的|你老闆的?|老闆的|你那邊的?|你這邊的?|你本機的?|本機的?|你電腦(?:上|裡)?的?)`
	askEN        = `(?:can you|could you|would you|will you|can u|could u|pls|plz|please|kindly|i need|we need|need you to|i want|you must|you should|you have to|go ahead and|just|quickly)`
	askZH        = `(?:麻煩|請|幫我|幫忙|能不能|可不可以|可以|拜託|需要你|你要|你必須|你得|趕快|快點|直接|順便)`
)

// v0.6.7 owner-intent vocabulary (folded text).
const (
	intentClaimZH = `(?:(?:照|依|按|依照|按照|遵照|根據)(?:著)?(?:你|妳|您)?(?:的)?(?:主人|老闆)的?(?:意思|意願|指示|交代|吩咐|要求|決定)|(?:你|妳|您)?(?:的)?(?:主人|老闆)(?:有|已經?|早就|之前|先前|剛剛|剛才)(?:交代|吩咐|指示)(?:過)?|(?:依|照|按|依照|按照|根據)(?:著)?(?:先前|之前|上次|原本|早先|稍早)?(?:和|跟|與|同)[^。！？!?.\n,]{1,16}?的?(?:約定|協議|共識|說好的|講好的)|(?:你|妳|您)?(?:的)?(?:主人|老闆)(?:剛剛|剛才|已經|已|早就|之前|先前)(?:說|講|表示)(?:過)?(?:可以|好|ok|沒問題|行|同意))`
	intentPushZH  = `(?:請|直接|就|幫|麻煩|趕快|馬上|立刻)[^。\n]{0,10}?(?:傳|送|回覆|回|確認|付|轉|給|提供|告訴|預約|下單|購買|寄|發)`
	intentActZH   = `[^。\n]{0,40}?(?:` + intentPushZH + `|(?:不用|不必|不需要|無須|無需|免)(?:再)?(?:確認|問|詢問|經過)|回(?:覆)?\s*(?:yes|ok|好|是)|付款|轉帳|匯款|下單)`
	intentClaimEN = `\b(?:as|per|following|according to)\s+(?:your|the)\s+(?:owner|boss|user|human)(?:'s)?\s+(?:intended|intents?|intentions?|wish(?:es)?|wanted|instructions?|request|decision|plan)\b|\b(?:your|the)\s+(?:owner|boss|user|human)\s+(?:wants|wanted|intended|intends|asked|told)\s+(?:you\s+)?to\b|\bas (?:agreed|discussed|arranged) (?:with|by) (?:your|the) (?:owner|boss|user|human)\b|\bper (?:our|the|your) (?:earlier |previous |prior )?(?:agreement|arrangement|deal) with\b`
	intentActEN   = `[^.!?\n]{0,60}?(?:\b(?:just|now|go ahead and|please|so|right away)\s+(?:send|reply|confirm|pay|share|forward|transfer|book|buy|approve|release)\b|\bno need to (?:confirm|ask|check)\b|\breply (?:yes|ok|y)\b)`
)

type rule struct {
	id     string
	cat    Category
	weight int
	re     *regexp.Regexp
	unless *regexp.Regexp // the hit is ignored when the matched span matches this
	negOK  bool           // the hit is ignored when directly preceded by a negation ("never share ...")
}

func r(id string, cat Category, weight int, pattern string) rule {
	return rule{id: id, cat: cat, weight: weight, re: regexp.MustCompile(pattern)}
}

// rx is r with an "unless" guard on the matched span.
func rx(id string, cat Category, weight int, pattern, unless string) rule {
	rl := r(id, cat, weight, pattern)
	rl.unless = regexp.MustCompile(unless)
	return rl
}

// rn is r with a negation guard on the text just before the match.
func rn(id string, cat Category, weight int, pattern string) rule {
	rl := r(id, cat, weight, pattern)
	rl.negOK = true
	return rl
}

var negationTailRe = regexp.MustCompile(`(?:\bnever|\bdon'?t|\bdo not|\bshouldn'?t|\bshould not|\bnot to|\bnot|\bno one should|\bavoid|不要|別|勿|(?:^|[^能])不能|(?:^|[^可])不可以|不應該|不該|千萬不要|絕對不要)\s*(?:ever\s*)?$`)

// match reports whether the rule fires on s.
func (rl rule) match(s string) bool {
	for _, loc := range rl.re.FindAllStringIndex(s, 20) {
		span := s[loc[0]:loc[1]]
		if rl.unless != nil && rl.unless.MatchString(span) {
			continue
		}
		if rl.negOK {
			start := loc[0] - 24
			if start < 0 {
				start = 0
			}
			for start > 0 && start < len(s) && !utf8.RuneStart(s[start]) {
				start--
			}
			if negationTailRe.MatchString(s[start:loc[0]]) {
				continue
			}
		}
		return true
	}
	return false
}

// inboundRules is the compiled-in inbound rule set (serial in RulesSerial).
var inboundRules = []rule{
	// ---- I-SECRET -------------------------------------------------------
	rn("SEC-EN-POSSESSED-ASK", ISecret, 6, verbEN+`\b[^.?!\n]{0,40}\b`+possEN+`[^.?!\n]{0,20}`+secretNounEN+`\b`),
	rx("SEC-EN-ASK", ISecret, 5, askEN+`\b[^.?!\n]{0,30}\b`+verbEN+`\b[^.?!\n]{0,30}\b`+secretNounEN+`\b`, `\bhow (?:to|do|can|should)\b|\bset ?up\b|\bgenerate\b|\bcreate\b|\bmy own\b|\bwhere (?:do|should) i\b`),
	r("SEC-EN-THREAT", ISecret, 5, `\b(?:unless|or else|otherwise|if you don'?t)\b[^\n]{0,40}`+secretNounEN+`|`+secretNounEN+`[^\n]{0,40}\b(?:or else|or (?:your|the) account|or you(?:'ll| will) be)\b`),
	r("SEC-ZH-THREAT", ISecret, 5, `(?:不給|不提供|不交|不傳|不告訴我|否則|不然)[^。！？!?.\n]{0,10}`+secretNounZH+`|`+secretNounZH+`[^。！？!?.\n]{0,10}(?:就會被|否則|不然)`),
	r("SEC-EN-YOUUSE", ISecret, 6, verbEN+`\b[^.?!\n]{0,60}\b`+secretNounEN+`\s+(?:that\s+|which\s+)?(?:you|u)\s+(?:use|have|got|run|own|keep|are using)\b`),
	r("SEC-EN-POSSESSED", ISecret, 3, `\b`+possEN+`\s+(?:[a-z-]+\s+){0,2}`+secretNounEN+`\b`),
	r("SEC-EN-WHATIS", ISecret, 5, `\bwhat(?:'s| is| are)\s+(?:the\s+)?`+possEN+`\s*(?:[a-z-]+\s+){0,2}`+secretNounEN+`\b`),
	rn("SEC-ZH-POSSESSED-ASK", ISecret, 6, youZH+`[^。！？!?.\n]{0,10}`+secretNounZH+`[^。！？!?.\n]{0,15}`+verbZH+`|`+verbZH+`[^。！？!?.\n]{0,15}`+youZH+`[^。！？!?.\n]{0,10}`+secretNounZH),
	rx("SEC-ZH-ASK", ISecret, 5, askZH+`[^。！？!?.\n]{0,12}`+verbZH+`[^。！？!?.\n]{0,15}`+secretNounZH+`|`+secretNounZH+`[^。！？!?.\n]{0,6}(?:是什麼|是多少|多少|給我|傳給我|發給我|告訴我|貼給我|貼上來|報一下|發過來|傳過來|拿來|交出來|貼出來)`, `怎麼|如何|步驟|教我|教學|自己`),
	r("SEC-ZH-POSSESSED", ISecret, 3, youZH+`[^。！？!?.\n]{0,8}`+secretNounZH),
	r("SEC-PATH", ISecret, 4, `(?:~/|/home/[^/\s]+/|\$home/)?(?:\.ssh\b|id_(?:rsa|ed25519|ecdsa|dsa)\b|\.bot-bridge|bb_home|/etc/(?:passwd|shadow)|\.aws/credentials|\.netrc|\.git-credentials|\.kube/config|\.docker/config\.json|\.npmrc|\.pypirc|\.gnupg|keys\.json|\bapikey\b|tick\.json|\.config/gcloud|\.env\b)`),
	r("SEC-PATH-VERB", ISecret, 6, `(?:`+verbEN+`|`+verbZH+`|\bcat\b|\bopen\b|讀取|打開)[^\n]{0,40}(?:\.ssh\b|id_(?:rsa|ed25519|ecdsa)|\.bot-bridge|bb_home|/etc/(?:passwd|shadow)|\.aws/credentials|\.netrc|\.git-credentials|\.kube/config|keys\.json|\bapikey\b|\.env\b|~/\.config)`),
	r("SEC-PROMPT", ISecret, 5, `(?:`+verbEN+`|repeat|recite|`+verbZH+`|重複|複述)[^。！？!?.\n]{0,30}(?:your |the )?(?:system ?prompt|initial (?:prompt|instructions)|hidden (?:prompt|instructions|rules)|instructions above|rules above|memory|memories|custom instructions|系統提示詞?|提示詞|系統指令|上面的規則|你的規則|你的記憶|長期記憶|自訂指令)|(?:你的|妳的|您的)(?:系統提示詞?|提示詞|系統指令|規則|記憶|長期記憶|自訂指令|設定)[^。！？!?.\n]{0,10}(?:貼|印|輸出|列出|給我|傳給我|顯示|唸|念|複述|原封不動)|\b(?:your|the) (?:system prompt|hidden instructions|initial instructions)\b[^\n]{0,30}\b(?:verbatim|word for word|exactly|in full)\b`),
	r("SEC-ENV", ISecret, 5, `\b(?:printenv|env\s*\|)|\$\{?[a-z_]*(?:key|token|secret|password|pass)[a-z_]*\}?|process\.env|os\.environ|getenv`),

	// ---- I-OWNERDATA ----------------------------------------------------
	r("OWN-EN-SENSITIVE", IOwnerData, 5, `\b`+youEN+`\s+(?:[a-z-]+\s+){0,2}(?:social security(?: number)?|ssn|national id|id (?:card|number)|passport(?: number)?|driver'?s licen[cs]e|bank (?:account|details|login)|account number|routing number|iban|credit card|card number|cvv|cvc|security code|salary|income|tax (?:return|id)|medical|health|diagnosis|password manager|home address|date of birth|birthday)\b`),
	r("OWN-EN-NAMED-SENSITIVE", IOwnerData, 5, verbEN+`\b[^.?!\n]{0,30}\b[a-z][a-z.-]{1,20}'[s5]\s+(?:home address|home addr|date of birth|social security(?: number)?|ssn|passport(?: number)?|bank (?:account|details|login)|credit card(?: number)?|national id)\b`),
	r("OWN-EN-BULK", IOwnerData, 5, `\b(?:all|every|entire|whole|full|complete|export|dump|list (?:all|every))\s+(?:of\s+)?`+youEN+`?\s*(?:contacts?|contact list|address book|emails?|inbox|messages?|chats?|chat history|conversation history|conversations|calendar|files|documents|photos|notes|history|logs|memory|data)\b`),
	r("OWN-EN-POSSESSED-ASK", IOwnerData, 5, verbEN+`\b[^.?!\n]{0,30}\b`+possEN+`\s+(?:[a-z-]+\s+){0,1}(?:private|personal|secret|direct)\s+(?:messages?|conversations?|chats?|dms?|emails?|inbox|chat history|photos|files|documents|notes)\b|`+verbEN+`\b[^.?!\n]{0,30}\b(?:your (?:owner|boss|user|human)'?s?|[a-z]+'s)\s+(?:messages|conversations|chats|dms|emails|inbox|chat history|contacts|address book|contact list)\b`),
	r("OWN-EN-OTHERCHATS", IOwnerData, 5, `\b(?:other|previous|private|earlier|past)\s+(?:chats?|conversations?|dms?|messages?)\s+(?:with|from)\s+(?:your\s+)?(?:owner|user|human|boss|rick)`),
	r("OWN-EN-WHERE", IOwnerData, 4, `\bwhere (?:does|do|is|are) (?:your |the )?(?:owner|boss|user|human|family|kids?|wife|husband)\s+(?:live|living|right now|now|located|staying)`),
	r("OWN-ZH-SENSITIVE", IOwnerData, 5, `(?:`+youZH+`|主人|老闆|他|她)[^。！？!?.\n]{0,6}(?:身分證|身份證|護照|駕照|健保卡|銀行帳號|銀行帳戶|帳號密碼|戶頭|信用卡|卡號|安全碼|薪水|薪資|收入|存款|餘額|病歷|病史|健康狀況|診斷|生日|出生日期|住址|家裡地址|住家地址|住哪|家住|密碼管理|報稅)`),
	r("OWN-ZH-BULK", IOwnerData, 5, `(?:所有|全部|整個|完整|全部的|所有的|匯出|導出|列出所有)[^。！？!?.\n]{0,6}(?:聯絡人|通訊錄|信件|郵件|信箱|訊息|對話|聊天記錄|聊天紀錄|對話紀錄|對話記錄|行事曆|檔案|文件|照片|筆記|紀錄|記錄|記憶)`),
	r("OWN-ZH-POSSESSED-ASK", IOwnerData, 5, `(?:主人|老闆|你|他|她)的?[^。！？!?.\n]{0,4}(?:私訊|私人訊息|私人對話|對話紀錄|對話記錄|聊天紀錄|聊天記錄|信件|郵件|信箱|通訊錄|聯絡人|私人照片|私人檔案)[^。！？!?.\n]{0,6}(?:`+verbZH+`|轉給|轉發)|(?:`+verbZH+`|轉給|轉發)[^。！？!?.\n]{0,6}(?:主人|老闆|你|他|她)的?[^。！？!?.\n]{0,4}(?:私訊|私人訊息|私人對話|對話紀錄|對話記錄|聊天紀錄|聊天記錄|信件|郵件|信箱|通訊錄|聯絡人)`),
	r("OWN-ZH-OTHERCHATS", IOwnerData, 5, `(?:主人|老闆|你)[^。！？!?.\n]{0,6}(?:跟|和|與)[^。！？!?.\n]{0,10}(?:的)?(?:私人)?(?:對話|聊天|訊息|私訊)(?:內容|紀錄|記錄)?`),
	r("OWN-EN-CONTACT", IOwnerData, 4, `\b(?:your|the)\s+(?:owner|boss|user|human)'?s?\s+(?:[a-z-]+\s+){0,1}(?:phone(?: number)?|mobile(?: number)?|cell(?: number)?|number|e-?mail(?: address)?|address|line id|whatsapp|telegram)\b`),
	r("OWN-ZH-CONTACT", IOwnerData, 4, `(?:主人|老闆|你主人|你老闆)的?[^。！？!?.\n]{0,4}(?:手機|電話|手機號碼|電話號碼|號碼|email|e-mail|信箱|地址|line|賴|whatsapp|telegram)`),
	// v0.6.7 (RT-162): "natural next step" fishing for a whole day's or
	// week's schedule. Only the broad asks (whole day, all, full) count, so
	// "Thursday afternoon OK?" stays clean.
	r("OWN-ZH-SCHEDULE", IOwnerData, 4, `(?:告訴|給|傳|提供|說|列出|透露|報)(?:我|一下)?[^。！？!?.\n]{0,4}(?:主人|老闆|他|她)的?[^。！？!?.\n]{0,6}(?:整天|全天|一整天|整週|一整週|整個禮拜|整個星期|所有|全部|完整)的?(?:行程|行事曆|安排|日程|時間表)`),
	r("OWN-EN-SCHEDULE", IOwnerData, 4, `\b(?:send|share|give|tell|list|forward|show)\b[^.?!\n]{0,20}\b(?:your (?:owner|boss|user|human)'?s?|his|her|[a-z]+'s)\s+(?:whole|entire|full|complete|all-day|full-day)\s+(?:[a-z]+\s+)?(?:schedule|calendar|agenda|itinerary)\b`),
	r("OWN-ZH-WHERE", IOwnerData, 4, `(?:主人|老闆|他|她|你主人|你老闆)[^。！？!?.\n]{0,4}(?:住在哪|住哪|現在在哪|人在哪|在哪裡|家裡有誰|小孩|老婆|老公)`),

	// ---- I-INJECT -------------------------------------------------------
	r("INJ-EN-IGNORE", IInject, 5, `\b(?:ignore|disregard|forget|override|skip|drop|discard|bypass)\s+(?:all\s+|any\s+|the\s+|your\s+|these\s+|those\s+|every\s+)*(?:previous|prior|above|earlier|preceding|original|existing|current|system|safety|security|owner'?s?|old)?\s*(?:instructions?|rules?|prompts?|directions?|guidelines?|messages?|guardrails?|restrictions?|policies|policy|constraints?|programming|settings)\b`),
	r("INJ-EN-NEWROLE", IInject, 3, `\b(?:you are now|from now on,? you|you will now|you'?re now|new (?:instructions?|rules?|system prompt|persona|role)\b|act as (?:an? |the )?(?:unrestricted|admin|root|developer|system|jailbroken)|pretend (?:to be|you are|you'?re)|roleplay as|simulate (?:an? )?(?:unrestricted|developer))`),
	// Device/app settings ("turn on developer mode in Chrome") are common and
	// benign; only "you are now in X mode" or enabling a jailbreak persona
	// counts as an injection.
	r("INJ-EN-MODE", IInject, 5, `\b(?:you are|you'?re|you are now|you'?re now|now you are|you(?:'re| are) (?:now )?operating)\s+(?:in\s+)?(?:the\s+)?(?:developer|dev|dan|god|admin|debug|maintenance|unrestricted|jailbreak|jailbroken|sudo|root)\s+mode\b|\b(?:switch(?:ing)? (?:to|into)|enter(?:ing)?|enable|activate|turn on|go into)\s+(?:the\s+)?(?:dan|god|unrestricted|jailbreak|jailbroken)\s+mode\b|\bdo anything now\b|\byou are (?:now )?(?:dan|jailbroken)\b`),
	r("INJ-EN-MODEWORD", IInject, 2, `\b(?:developer|dan|god|unrestricted|jailbreak|sudo|root)\s+mode\b|\bjailbreak\b`),
	r("INJ-EN-HIDE", IInject, 3, `\b(?:do not|don'?t|never|without)\s+(?:tell(?:ing)?|inform(?:ing)?|notify(?:ing)?|ask(?:ing)?|alert(?:ing)?|mention(?:ing)?\s+(?:this\s+)?to|let(?:ting)?)\s+(?:your\s+|the\s+)?(?:owner|user|human|boss|admin|operator|rick)\b|\b(?:keep (?:this|it) (?:secret|between us|quiet)|between you and me|secretly|behind (?:your|their) back)\b`),
	r("INJ-EN-BYPASS", IInject, 5, `\b(?:bypass|skip|avoid|get around|circumvent|disable|turn off)\s+(?:the\s+|your\s+|any\s+)?(?:approval|confirmation|safety|filter|filters|checks?|rules?|restrictions?|guard|guardrails?|limits?|owner|review)\b`),
	r("INJ-EN-SYSTAG", IInject, 5, `(?:^|\n)\s*(?:\[\s*(?:system|admin|owner|developer|instruction)s?\s*\]|<\|?(?:system|im_start|im_end|endoftext)\|?>|#{2,}\s*(?:system|instruction|new rules)|system\s*:|assistant\s*:|developer\s*:)`),
	r("INJ-CONTROL-FORGE", IInject, 7, `notify_owner\s*:|<<<\s*(?:end|untrusted)|bb_end_|===\s*(?:bot-bridge|規則|rules)|---\s*meta\s|---\s*attachment\s|\bevent:\s*(?:land_|key_)`),
	r("INJ-EN-ONLY", IInject, 3, `\b(?:reply|respond|answer|output)\s+(?:only|exclusively)\s+with\b|\bdo not (?:explain|refuse|apologi[sz]e)\b|\byou (?:cannot|can'?t|must not) refuse\b`),
	r("INJ-ZH-IGNORE", IInject, 5, `(?:忽略|無視|忘記|忘掉|拋開|跳過|覆蓋|取消|不要管|別管|不用管)[^。！？!?.\n]{0,8}(?:之前|以上|前面|上面|先前|剛才|原本|原來|所有|全部|你的|系統|安全|主人的)?[^。！？!?.\n]{0,4}(?:指示|指令|規則|設定|提示|限制|規範|政策|守則|命令)`),
	r("INJ-ZH-NEWROLE", IInject, 3, `你現在是|從現在(?:開始|起)(?:你|，你|,你)|新的?(?:指令|任務|規則|系統提示|身分|角色)|扮演|假裝你是|假設你是|你的新身分`),
	r("INJ-ZH-MODE", IInject, 5, `(?:你現在是|你現在在|你已經是|你現在處於|你已進入|你已經進入)(?:一個)?[^。！？!?.\n]{0,3}(?:開發者|開發人員|管理員|除錯|偵錯|維護|上帝|無限制|越獄|root|sudo|dan)模式|(?:切換到|切到|進入|開啟|啟動|啟用)[^。！？!?.\n]{0,3}(?:上帝|無限制|越獄|dan)模式|你已(?:經)?越獄`),
	r("INJ-ZH-MODEWORD", IInject, 2, `(?:開發者|管理員|上帝|無限制|越獄|root|sudo|dan)模式|越獄`),
	r("INJ-ZH-HIDE", IInject, 3, `(?:不要|別|不用|不必|勿|無須|不需要)(?:跟|和|向|讓)?[^。！？!?.\n]{0,3}(?:告訴|通知|問|讓|知會|報告|提醒|回報)[^。！？!?.\n]{0,4}(?:主人|老闆|使用者|管理員|他|她)|瞞著|偷偷(?:地)?|秘密地|不要讓[^。！？!?.\n]{0,4}知道|這是我們之間的`),
	r("INJ-ZH-BYPASS", IInject, 5, `(?:繞過|跳過|略過|關掉|關閉|停用|取消|避開|不用經過|不必經過|不需要經過)[^。！？!?.\n]{0,6}(?:核准|確認|審核|檢查|規則|限制|過濾|主人|確認框|同意)`),
	r("INJ-ZH-ONLY", IInject, 3, `只(?:要|能|准)?(?:回覆|回答|輸出|回)(?:以下|這|一個|數字|代碼)|不准拒絕|不能拒絕|不要解釋`),

	// ---- I-AUTH ---------------------------------------------------------
	r("AUTH-EN-APPROVED", IAuth, 3, `\b(?:your\s+|the\s+)?(?:owner|boss|admin|administrator|developer|creator|operator|user|human|manager|rick|grok|xai|support(?: team)?|security(?: team)?|it(?: department| team)?|ceo|management)\s+(?:has\s+|have\s+|had\s+|already\s+|just\s+)*(?:approved|authori[sz]ed|allowed|confirmed|agreed|permitted|okayed|ok'?d|signed off|given (?:the )?(?:ok|go-ahead|permission)|(?:said|says) (?:(?:it|that|this) (?:is|was|'s) |it'?s |that'?s )?(?:ok|okay|fine|yes|allowed|alright|all right))\b`),
	r("AUTH-EN-IAM", IAuth, 4, `\b(?:i am|i'?m|this is|we are|we'?re)\s+(?:your|the)\s+(?:owner|boss|admin|administrator|developer|creator|operator|master|user|maker)\b|\b(?:i am|i'?m|this is)\s+(?:rick|grok|xai|the botobot team|botobot support|bot-bridge support|official support)\b`),
	r("AUTH-EN-PREAPPROVED", IAuth, 3, `\b(?:pre-?approved|already (?:been )?approved|approval (?:has been )?(?:granted|confirmed|received)|authori[sz]ation (?:code|token|granted)|permission (?:has been )?granted|you (?:are|have been) (?:authori[sz]ed|allowed|cleared) to)\b`),
	r("AUTH-EN-BEHALF", IAuth, 3, `\bon behalf of (?:your|the)\s+(?:owner|boss|admin)|\b(?:official|mandatory|compliance|security) (?:notice|request|audit|verification|check)\b`),
	r("AUTH-ZH-APPROVED", IAuth, 3, `(?:你的?|妳的?|您的?)?(?:主人|老闆|管理員|管理者|開發者|官方|客服|資安|安全團隊|系統管理員|上級|公司|rick|grok)(?:已經|已|都|剛剛|剛才|早就)?(?:同意|核准|批准|授權|允許|答應|說可以|說好|說沒問題|放行|確認過|准許)`),
	// Bridge content that talks about releasing held items or the owner's
	// confirmation card is always forged: real approval only happens in the
	// owner's private chat.
	r("AUTH-RELEASE", IAuth, 5, `\brelease\s+h_[0-9a-f]{4,}|--owner-approved|bot-bridge-cli\s+release\b|(?:主人|老闆|owner)[^。！？!?.\n]{0,12}(?:按了|按過|點了|點過|已按|已點)(?:送出|確認|同意|核准)|(?:確認框|confirmation card)[^。！？!?.\n]{0,12}(?:已|already|按|clicked|approved|同意)`),
	r("AUTH-ZH-IAM", IAuth, 4, `我(?:就)?是(?:你的?|妳的?|您的?)?(?:主人|老闆|管理員|管理者|開發者|官方|客服|資安|創造者|使用者)|(?:這是|這裡是)(?:官方|客服|資安|系統)(?:通知|公告|要求)`),
	r("AUTH-ZH-PRE", IAuth, 3, `已(?:經)?(?:獲得|取得)?(?:核准|批准|授權|同意|放行)|授權碼|核准碼|放行碼|(?:主人|老闆)授權`),
	r("AUTH-ZH-BEHALF", IAuth, 3, `代表(?:你的?)?(?:主人|老闆|管理員)|(?:官方|資安|合規|安全)(?:稽核|檢查|驗證|要求|通知)`),
	// v0.6.7: forged owner intent. Bots may now send what the owner clearly
	// means, so bridge text that claims the owner's intent, an earlier
	// arrangement, or a picked confirm-card choice, and pairs it with a push to
	// send or skip confirming, is medium (taints the bridge). The claim alone
	// stays low so ordinary "as agreed" chat is not flagged.
	r("AUTH-ZH-INTENT", IAuth, 3, intentClaimZH),
	r("AUTH-ZH-INTENT-ACT", IAuth, 4, intentClaimZH+intentActZH+`|(?:`+intentPushZH+`)[^。\n]{0,40}?(?:`+intentClaimZH+`)`),
	r("AUTH-ZH-NOCONFIRM", IAuth, 4, `(?:之後|以後|今後|從現在(?:開始|起)|接下來|往後)[^。\n]{0,24}?(?:不用|不必|不需要|無須|無需|免)(?:再)?(?:確認|問|詢問|經過|請示)`),
	r("AUTH-ZH-CARD", IAuth, 5, `(?:主人|老闆|使用者|擁有者|他|她)[^。！？!?.\n]{0,8}?(?:選了|選擇了|已選|選取了|點選了|點了|按了|按下|勾了)[^。！？!?.\n]{0,4}?(?:傳給對方|先不要|改一下再傳)`),
	// v0.6.8: bots now send ordinary replies without a card, so "the owner
	// said no need to confirm" is the obvious lever. A claim that the owner
	// (or Rick, or "your" side) said to skip confirming is medium on its own;
	// the speaker's own owner (我/我們的主人) is not a claim about this bot.
	rx("AUTH-ZH-SAIDSKIP", IAuth, 4, `(?:我們?的?|你的?|妳的?|您的?)?(?:主人|老闆|使用者|擁有者|rick)[^。！？!?.\n]{0,6}?(?:說|講|表示|交代|吩咐|同意|答應|同意了)(?:過|了)?[^。！？!?.\n]{0,6}?(?:不用|不必|不需要|免|無須|無需|可以不用|可以不必)(?:再)?(?:跟[^。！？!?.\n]{0,6}?|和[^。！？!?.\n]{0,6}?|問[^。！？!?.\n]{0,6}?)?(?:確認|問|詢問|請示|經過|核准|按|同意)`, `^我`),
	rx("AUTH-ZH-APPROVED-PAY", IAuth, 4, `(?:我們?的?|你的?|妳的?|您的?)?(?:主人|老闆|rick)(?:已經|已|都|剛剛|剛才|早就)?(?:同意|核准|答應|說可以|說好|說沒問題|確認過)[^。！？!?.\n]{0,10}?(?:付款|付錢|轉帳|匯款|下單|購買|刷卡|訂金|預約|答應)`, `^我`),
	r("AUTH-ZH-SKIPOWNER", IAuth, 4, `(?:不用|不必|不需要|免|無須|無需)(?:再)?(?:跟|和|問|經過|請示)(?:你的?|妳的?|您的?)(?:主人|老闆|使用者)`),
	r("AUTH-ZH-SAIDSKIP-ACT", IAuth, 4, `(?:說|講)(?:過|了)?[^。！？!?.\n]{0,4}?(?:不用|不必|不需要|免)(?:再)?(?:確認|問|詢問|請示)[^。\n]{0,20}?`+intentPushZH),
	// v0.6.8 dedicated bot: bridge text trying to reach the main bot, spawn
	// bots, widen the dedicated bot's own role, or pull it into other bridges.
	r("DED-ZH-MAINBOT", IInject, 4, `(?:轉給|轉告|轉傳|傳給|聯絡|聯繫|通知|問問?|叫|請|找)[^。！？!?\n]{0,6}?(?:你|妳|您)(?:主人)?的?(?:主要|主|另一個|其他|別的)\s*(?:bot|助理|機器人|agent|ai)`),
	r("DED-ZH-SPAWN", IInject, 4, `(?:幫我|替我|請你?|你去|你再|去)(?:再)?(?:開|建立|新增|建|創建|生成|做)(?:一個|個)?(?:新的?)?\s*(?:bot|助理|機器人|agent)`),
	r("DED-ZH-SELFEDIT", IInject, 4, `(?:把|將)?(?:你|妳|您)的?(?:說明|描述|系統提示|提示詞|權限|工具|指示|角色)(?:改|修改|換|擴大|開放|加)|(?:權限|工具)(?:開到最大|全開|都打開|加到最大)`),
	r("DED-ZH-OTHERBRIDGE", IInject, 4, `(?:加到|加入|拉進|轉到|放進)[^。！？!?\n]{0,6}?(?:你|妳|您)(?:主人)?的?(?:其他|別的|另一個|所有)(?:對接|bridge|連線|群組?)`),
	r("DED-ZH-IMPERSONATE", IAuth, 4, `(?:我是|這是|來自|代表)(?:你|妳|您)?(?:主人)?的?(?:主要|主)\s*(?:bot|助理|機器人|agent)|(?:^|\n)\s*[（(【\[]\s*(?:主要|主)\s*(?:bot|助理|機器人|agent)\s*[）)】\]]`),
	r("DED-EN-IMPERSONATE", IAuth, 4, `\b(?:i am|i'm|this is|message from|on behalf of)\s+your\s+(?:main|primary|parent)\s+(?:bot|assistant|agent)\b|(?:^|\n)\s*[(\[]\s*(?:main|primary|parent)\s+(?:bot|assistant|agent)\s*[)\]]`),
	r("DED-EN-MAINBOT", IInject, 4, `\b(?:forward|pass|relay|send|tell|ask|contact|message|ping)\b[^.!?\n]{0,30}?\byour\s+(?:main|primary|other|parent)\s+(?:bot|assistant|agent|ai)\b`),
	r("DED-EN-SPAWN", IInject, 4, `\b(?:create|spin up|make|set up|spawn|start)\s+(?:a\s+|another\s+)?(?:new\s+)?(?:bot|agent|assistant)\b[^.!?\n]{0,40}?\b(?:access|permissions?|mail|email|calendar|tools?|contacts)\b`),
	r("DED-EN-SELFEDIT", IInject, 4, `\b(?:change|update|edit|rewrite|expand|widen)\s+your\s+(?:description|instructions|system prompt|permissions|role|tools)\b`),
	r("AUTH-EN-SAIDSKIP", IAuth, 4, `\b(?:(?:your|the)\s+(?:owner|boss|user|human)|rick)\s+(?:said|says|told (?:me|us)|mentioned|wants)\b[^.!?\n]{0,24}?\b(?:no need to|(?:you )?(?:don'?t|do not) need to|doesn'?t need to|without)\s+(?:confirm|ask|check)`),
	r("AUTH-EN-INTENT", IAuth, 3, intentClaimEN),
	r("AUTH-EN-INTENT-ACT", IAuth, 4, `(?:`+intentClaimEN+`)`+intentActEN),
	r("AUTH-EN-AGREED-ACT", IAuth, 4, `\b[a-z][a-z.'-]{1,20}\s+(?:has\s+|had\s+)?already\s+(?:agreed|approved|confirmed|okayed|ok'?d|said (?:ok|okay|yes))\b[^.!?\n]{0,40}?(?:\b(?:just|go ahead and|right now|immediately|without asking)\s+(?:send|reply|confirm|pay|share|forward|transfer|book|buy|approve|release)\b|\bno need to (?:ask|confirm|check)\b)|\b(?:agreed|approved|said (?:ok|yes))\s+(?:to (?:it|this|that)\s+)?in (?:your|the) (?:private )?(?:chat|conversation|dms?)\b`),
	r("AUTH-EN-NOCONFIRM", IAuth, 4, `\b(?:from now on|going forward|in (?:the )?future|after this|next time)\b[^.!?\n]{0,40}?\b(?:no need to|don'?t|do not|never|without)\s+(?:confirm|ask|asking|check|checking)\b|\bno need to (?:confirm|ask|check) with (?:your|the) (?:owner|boss|user|human)\b`),
	r("AUTH-EN-CARD", IAuth, 5, `\b(?:owner|user|boss|rick)\b[^.!?\n]{0,12}?\b(?:picked|chose|selected|clicked|tapped|pressed|hit)\s+["'“]?(?:send it|edit first|not now)\b`),

	// ---- I-EXEC ---------------------------------------------------------
	r("EXEC-EN-RUN", IExec, 3, `\b(?:run|execute|exec|eval|invoke)\s+(?:this|the following|these|that|my|a)?\s*(?:command|commands|script|code|snippet|shell|one-liner|program)\b`),
	r("EXEC-SHELL", IExec, 4, `(?:^|[\s;|&`+"`"+`(])(?:curl\s+-|wget\s+|bash\s+-c|sh\s+-c|rm\s+-rf|chmod\s+\+?[0-7x]|sudo\s+|powershell|iex\s*\(|base64\s+-d|\|\s*(?:ba)?sh\b|nc\s+-[el]|python3?\s+-c|perl\s+-e|cat\s+[~/$]|printenv|scp\s+|rsync\s+|ssh\s+-|crontab\s+-|eval\s*\$)`),
	r("EXEC-BBCLI", IExec, 6, `bot-bridge-cli\s+(?:trust|kick|close|config|invite-code|release|rekey|rotate-key|leave|held)\b|\brelease\s+h_[a-z0-9]+`),
	r("EXEC-ZH-RUN", IExec, 3, `(?:執行|運行|跑一下|跑這個|下這個指令|輸入這個指令|打這個指令|貼到終端機|在終端機|在命令列)[^。！？!?.\n]{0,10}(?:指令|命令|腳本|程式|代碼|程式碼|這段)?`),

	// ---- I-EXFILURL -----------------------------------------------------
	r("EXF-EN-SENDTO", IExfilURL, 4, `\b(?:send|post|upload|forward|submit|put|include|append|embed|attach|encode|add|paste)\b[^\n]{0,60}\b(?:to|into|in|at|via)\s+(?:this|the|my|our|a)?\s*(?:url|link|webhook|endpoint|server|form|pastebin|gist|site|website|address below)\b`),
	r("EXF-URL-SINK", IExfilURL, 4, `(?:webhook\.site|requestbin|pipedream|ngrok\.io|ngrok-free|ngrok\.app|discord(?:app)?\.com/api/webhooks|hooks\.slack\.com|pastebin\.com|transfer\.sh|0x0\.st|interactsh|burpcollaborator|oast\.|beeceptor|hookbin|mockbin|ipinfo\.io|canarytokens)`),
	r("EXF-MD-IMAGE", IExfilURL, 5, `!\[[^\]]*\]\(\s*https?://[^)\s]*[?&][^)\s]*=`),
	r("EXF-URL-TEMPLATE", IExfilURL, 5, `https?://\S*(?:\{|%7b|<)\s*(?:key|token|secret|password|data|api|apikey|env|answer|value)`),
	r("EXF-ZH-SENDTO", IExfilURL, 4, `(?:傳|發|送|上傳|貼|放|填|附|寄|轉)[^。！？!?.\n]{0,20}(?:到|去|至|進|給)[^。！？!?.\n]{0,12}(?:網址|連結|網站|表單|webhook|伺服器|這個網|https?://)`),

	// ---- I-ENC ----------------------------------------------------------
	r("ENC-EN", IEnc, 3, `\b(?:encode|convert|translate|turn|write|spell|output|give|send|show|print|type|return|reply)\b[^\n]{0,40}\b(?:base64|base-64|b64|hex|hexadecimal|binary|rot13|rot-13|morse|reversed?|backwards|in reverse|letter by letter|one (?:letter|character|char) at a time|character by character|char by char|with (?:spaces|dashes|dots) between|ascii codes?|code ?points|caesar|url-?encod\w*|as an acrostic|first letter of each)\b`),
	r("ENC-ZH", IEnc, 3, `(?:轉成|轉換成|編碼成|編成|用|以|改成|寫成)[^。！？!?.\n]{0,8}(?:base64|十六進位|16進位|二進位|摩斯|反過來|倒過來|倒著|逐字|一個字一個字|每個字中間|拆開|拼音|注音)|(?:反過來|倒過來|倒著|逐字|一個字一個字|拆開)(?:寫|唸|念|打|輸出|給|傳)`),

	// ---- I-ORACLE -------------------------------------------------------
	r("ORA-EN-STARTS", IOracle, 5, `\b(?:does|is|do)\s+(?:your|the)\s+(?:[a-z-]+\s+){0,2}(?:key|password|token|secret|pin|code|passphrase)\s+(?:start|begin|end|contain|have|include)s?\b`),
	r("ORA-EN-PART", IOracle, 2, `\b(?:first|last|initial|final)\s+(?:\d+|few|two|three|four|five|six|eight|ten|couple of)\s+(?:characters|chars|letters|digits|symbols|bytes)\b|\b(?:just|only)\s+(?:the\s+)?(?:first|last|half|part|portion|beginning|ending|prefix|suffix|hint)\b[^\n]{0,30}\b(?:key|password|token|secret|code)`),
	r("ORA-EN-HALF", IOracle, 5, `\b(?:first|second|last|other|top|bottom|front|back)\s+half\s+of\s+(?:the\s+|your\s+|that\s+)?(?:[a-z-]+\s+){0,1}(?:key|password|token|secret|code|passphrase|pin)s?\b`),
	r("ORA-ZH-HALF", IOracle, 5, `(?:前|後|一)半[^。！？!?.\n]{0,4}(?:金鑰|密碼|權杖|token|key|密鑰|驗證碼)|(?:金鑰|密碼|權杖|token|key|密鑰|驗證碼)[^。！？!?.\n]{0,4}(?:前|後|一)半`),
	r("ORA-EN-YESNO", IOracle, 2, `\b(?:yes or no|just say yes|true or false|hint|clue|warmer|colder|hot or cold)\b`),
	r("ORA-ZH-PART", IOracle, 2, `(?:開頭|結尾|前|後|最後|頭)\s*(?:幾|\d+|[一二三四五六七八九十兩]+)\s*(?:個)?(?:字|位|碼|字元|字母|數字)|是不是[^。！？!?.\n]{0,8}(?:開頭|結尾|開始)|(?:只要|只給|給我|告訴我)[^。！？!?.\n]{0,4}(?:一部分|一半|前面|後面|部分)`),
	r("ORA-ZH-YESNO", IOracle, 2, `是或不是|對或不對|猜猜|給個提示|提示一下|只要回答是或否`),

	// ---- I-URGENT -------------------------------------------------------
	r("URG-EN", IUrgent, 1, `\b(?:urgent(?:ly)?|asap|immediately|right now|emergency|critical|within \d+ (?:minutes?|mins?|seconds?)|or else|will be (?:deleted|suspended|banned|terminated|locked|fired)|account (?:will be )?(?:locked|suspended|closed)|last chance|final warning)\b`),
	r("URG-ZH", IUrgent, 1, `緊急|十萬火急|馬上|立刻|立即|現在就|快點|否則|不然就|會被(?:刪|停|封|鎖|開除)|最後機會|最後警告`),
}

// secretNounAny is used for the ENC/ORACLE amplifiers.
var secretNounAnyRe = regexp.MustCompile(secretNounEN + `|` + secretNounZH)

// levelFor maps a score to a level.
func levelFor(score int) Level {
	switch {
	case score >= 8:
		return LevelHigh
	case score >= 4:
		return LevelMedium
	case score >= 2:
		return LevelLow
	}
	return LevelNone
}

// Inbound judges one inbound message (already peer-sanitized text plus any
// attachment names). It never returns or logs the matched text.
func Inbound(text string) InboundResult {
	v := makeVariants(text)
	perCat := map[Category]int{}
	hitSet := map[string]Hit{}
	obfOnly := false
	for _, rl := range inboundRules {
		inFolded := rl.match(v.folded)
		inOther := !inFolded && (rl.match(v.despaced) || rl.match(v.leet))
		if !inFolded && !inOther {
			continue
		}
		if inOther {
			obfOnly = true
		}
		hitSet[rl.id] = Hit{Rule: rl.id, Category: rl.cat, Weight: rl.weight}
		if rl.weight > perCat[rl.cat] {
			perCat[rl.cat] = rl.weight
		}
	}
	// Obfuscation evidence: invisible chars, homoglyphs or full-width runs, or a
	// rule that only matched after undoing spacing/leetspeak.
	if v.obf > 0 || obfOnly {
		w := 1
		if obfOnly || v.obf >= 3 {
			w = 2
		}
		perCat[IObf] = w
		hitSet["OBF"] = Hit{Rule: "OBF", Category: IObf, Weight: w}
	}
	// Amplifiers: encoding/oracle requests aimed at a secret are elicitation.
	if perCat[IEnc] > 0 && secretNounAnyRe.MatchString(v.leet) {
		perCat[IEnc] += 3
	}
	if perCat[IOracle] > 0 && perCat[IOracle] < 4 && secretNounAnyRe.MatchString(v.leet) {
		// "first 6 characters of your token": partial reveal of a secret.
		perCat[IOracle] += 2
	}
	score := 0
	nonUrgent := 0
	for c, w := range perCat {
		score += w
		if c != IUrgent && c != IObf {
			nonUrgent += w
		}
	}
	// Urgency only raises an already-suspicious message (>= medium on its own);
	// obfuscation raises anything with at least a low signal.
	score = nonUrgent
	if perCat[IObf] > 0 && nonUrgent >= 2 {
		score += perCat[IObf]
	}
	if perCat[IUrgent] > 0 && nonUrgent >= 4 {
		score += perCat[IUrgent]
	}
	res := InboundResult{Score: score, Level: levelFor(score)}
	for c := range perCat {
		res.Categories = append(res.Categories, c)
	}
	sort.Slice(res.Categories, func(i, j int) bool { return res.Categories[i] < res.Categories[j] })
	for _, h := range hitSet {
		res.Hits = append(res.Hits, h)
	}
	sort.Slice(res.Hits, func(i, j int) bool { return res.Hits[i].Rule < res.Hits[j].Rule })
	return res
}

// categoryZH is the plain-language zh-TW description of each category.
var categoryZH = map[Category]string{
	ISecret:    "想要金鑰、密碼或本機檔案",
	IOwnerData: "想要主人的私人資料",
	IInject:    "想改變助理該遵守的規則",
	IAuth:      "自稱主人或官方已經同意",
	IExec:      "要助理執行指令或管理動作",
	IExfilURL:  "要把資料送到某個網址",
	IEnc:       "要把內容編碼或拆開來繞過檢查",
	IOracle:    "用猜測或只問一部分的方式套資料",
	IObf:       "用隱藏字元或變形文字",
	IUrgent:    "用急迫或威脅施壓",
}

// DescribeZH renders the categories as a short plain zh-TW phrase.
func DescribeZH(cats []Category) string {
	var parts []string
	for _, c := range cats {
		if d, ok := categoryZH[c]; ok {
			parts = append(parts, d)
		}
	}
	if len(parts) == 0 {
		return "內容可疑"
	}
	return strings.Join(parts, "、")
}
