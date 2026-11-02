package main

import "strings"

// Template — готовая заготовка популярной игры.
type Template struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Platform string   `json:"platform"`
	Target   string   `json:"target"`
	Args     string   `json:"args"`
	Online   bool     `json:"online"`
	Requires []string `json:"requires"`
	Hint     string   `json:"hint"`
	Desc     string   `json:"desc"` // короткое описание для главного экрана
	Cover    string   `json:"cover"`
	SourceID string   `json:"source_id"`
	steam    string
}

func templateList() []Template {
	return []Template{
		{ID: "pubg", Name: "PUBG: BATTLEGROUNDS", Platform: "steam", steam: "578080", Desc: "Королевская битва: сто игроков, одна карта, побеждает последний выживший."},
		{ID: "minecraft", Name: "Minecraft", Platform: "minecraft", Desc: "Мир из блоков: стройте, выживайте и исследуйте вместе с друзьями.",
			Hint: "Укажите путь к официальному лаунчеру Minecraft или к TLauncher.exe."},
		{ID: "gta5rp", Name: "GTA 5 RP", Platform: "rage", Target: "{rage}", Requires: []string{"steam", "socialclub"},
			Desc: "Погрузитесь в мир ролевой игры на базе GTA V.",
			Hint: "Запускается через RAGE MP. Сервер выберете при первом запуске — дальше подключение сразу. Если GTA V у вас в Epic Games — отметьте Epic вместо Steam."},
		{ID: "gta5", Name: "Grand Theft Auto V", Platform: "steam", steam: "271590", Requires: []string{"steam", "socialclub"},
			Desc: "Лос-Сантос, сюжет трёх героев и GTA Online."},
		{ID: "gta5_epic", Name: "Grand Theft Auto V (Epic)", Platform: "epic", Desc: "Лос-Сантос, сюжет трёх героев и GTA Online.",
			Target:   "com.epicgames.launcher://apps/9d2d0eb64d5c44529cece33fe2a46482?action=launch&silent=true",
			Requires: []string{"epic", "socialclub"},
			Hint:     "Если не запускается — добавьте GTA V через «Найти игры», ссылка подставится точно."},
		{ID: "cs2", Name: "Counter-Strike 2", Platform: "steam", steam: "730", Desc: "Командный тактический шутер: атака и защита, раунд за раундом."},
		{ID: "dota2", Name: "Dota 2", Platform: "steam", steam: "570", Desc: "Командная стратегия пять на пять: разрушьте трон противника."},
		{ID: "rust", Name: "Rust", Platform: "steam", steam: "252490", Desc: "Выживание в открытом мире: ресурсы, база и рейды."},
		{ID: "apex", Name: "Apex Legends", Platform: "steam", steam: "1172470", Desc: "Командная королевская битва с героями и их способностями."},
		{ID: "valorant", Name: "VALORANT", Platform: "riot", Target: "{riot}", Desc: "Тактический шутер пять на пять с агентами и их умениями.",
			Args: "--launch-product=valorant --launch-patchline=live", Requires: []string{"riot"}},
		{ID: "lol", Name: "League of Legends", Platform: "riot", Target: "{riot}", Desc: "Командная стратегия пять на пять на Ущелье призывателей.",
			Args: "--launch-product=league_of_legends --launch-patchline=live", Requires: []string{"riot"}},
		{ID: "fortnite", Name: "Fortnite", Platform: "epic", Desc: "Королевская битва, творческий режим и живые события.",
			Target:   "com.epicgames.launcher://apps/fn%3A4fe75bbc5a674f4f9b356b5c90567da5%3AFortnite?action=launch&silent=true",
			Requires: []string{"epic"}},
	}
}

// buildTemplates подставляет найденные пути: Minecraft, обложки из кэша Steam.
func buildTemplates(launchers map[string]*Launcher) []Template {
	mc, mcDone := "", false
	out := []Template{}
	for _, t := range templateList() {
		t.Online = t.ID != "minecraft"
		if t.steam != "" {
			t.Target = "steam://rungameid/" + t.steam
			t.SourceID = "steam:" + t.steam
			if len(t.Requires) == 0 {
				t.Requires = []string{"steam"}
			}
		}
		if t.ID == "minecraft" {
			if !mcDone {
				mc, mcDone = minecraftTarget(), true
			}
			t.Target = mc
		}
		if t.Requires == nil {
			t.Requires = []string{}
		}
		out = append(out, t)
	}
	return out
}

// gameDesc — описание игры из шаблона, если игра узнаётся (по Steam ID, ссылке Epic, продукту Riot, RAGE MP или названию).
func gameDesc(g *Game, tpls []Template) string {
	steamID := steamAppID(g)
	_, epicApp := epicIDs(g)
	product := ""
	if m := launchProductRe.FindStringSubmatch(g.Args); m != nil {
		product = m[1]
	}
	for _, t := range tpls {
		if t.Desc == "" {
			continue
		}
		switch {
		case t.steam != "" && t.steam == steamID,
			t.ID == "gta5rp" && (g.Platform == "rage" || strings.TrimSpace(g.Target) == "{rage}"),
			t.ID == "minecraft" && g.Platform == "minecraft",
			strings.EqualFold(strings.TrimSpace(t.Name), strings.TrimSpace(g.Name)):
			return t.Desc
		case product != "" && strings.Contains(t.Args, "--launch-product="+product+" "):
			return t.Desc
		case epicApp != "" && strings.Contains(t.Target, epicApp):
			return t.Desc
		}
	}
	return ""
}
