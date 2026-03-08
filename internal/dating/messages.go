package dating

const (
	StatusStopped = "💤 Dating bot остановлен"
)

const (
	PatternLikedYou                     = "person liked you"
	PatternWriteMessage                 = "Write a message"
	PatternViewProfiles                 = "View profiles"
	PatternInterstitialSubscribeChannel = "subscribe to my channel"
	PatternInterstitialInternetSafety   = "please note that people on the internet"
	PatternInterstitialTikTokPromo      = "do you want more views on tiktok? #leomatch"
	PatternTooLong                      = "message is too long"
	PatternTooShort                     = "message is too short"
	PatternDailyLimitExact              = "Too many ❤️ today.\n\nInvite friends to get more ❤️!\n\nShare it with your friends/on your social media!\nYour personal link👇"
	PatternTooManyLikes                 = "too many"
)

const (
	MaxRetries   = 2
	MaxMsgLength = 220
)

type RetryType int

const (
	RetryTooLong RetryType = iota
	RetryTooShort
)

const TooLongRetryPrompt = `Перепиши это сообщение КОРОЧЕ. МАКСИМУМ 220 символов! Это критически важно.
Оставь только суть, убери лишние слова.

Сообщение для сокращения:
`

const TooShortRetryPrompt = `Предыдущее сообщение было слишком коротким или некорректным.
Напиши НОВОЕ персонализированное сообщение на основе профиля.
Сообщение должно быть:
- Минимум 50 символов
- Максимум 220 символов  
- Персонализированным и интересным
- На русском языке

НЕ пиши просто "привет" или короткие фразы!
`

const (
	ButtonViewProfiles = "1"
	ButtonMyProfile    = "2"
	ButtonStopSearch   = "3"
	ButtonInvite       = "4"
	ButtonLike         = "❤️"
	ButtonLikeMessage  = "💌 / 📹"
	ButtonDislike      = "👎"
	ButtonSleep        = "💤"
)
