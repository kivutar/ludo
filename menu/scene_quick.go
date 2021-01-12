package menu

import (
	"github.com/libretro/ludo/audio"
	"github.com/libretro/ludo/core"
	ntf "github.com/libretro/ludo/notifications"
	"github.com/libretro/ludo/state"
	"github.com/libretro/ludo/utils"
)

type sceneQuick struct {
	entry
}

func buildQuickMenu() Scene {
	var list sceneQuick
	list.label = "Quick Menu"

	list.children = append(list.children, entry{
		label: "Resume",
		icon:  "resume",
		callbackOK: func() {
			state.Global.MenuActive = false
			state.Global.FastForward = false
		},
	})

	list.children = append(list.children, entry{
		label: "Reset",
		icon:  "reset",
		callbackOK: func() {
			state.Global.Core.Reset()
			state.Global.MenuActive = false
			state.Global.FastForward = false
		},
	})

	list.children = append(list.children, entry{
		label: "Close",
		icon:  "close",
		callbackOK: func() {
			core.Unload()
			audio.PlayEffect(audio.Effects["cancel"])
			menu.stack[len(menu.stack)-2].segueBack()
			menu.stack = menu.stack[:len(menu.stack)-1]
		},
	})

	list.children = append(list.children, entry{
		label: "Savestates",
		icon:  "states",
		callbackOK: func() {
			list.segueNext()
			menu.Push(buildSavestates())
		},
	})

	list.children = append(list.children, entry{
		label: "Take Screenshot",
		icon:  "screenshot",
		callbackOK: func() {
			name := utils.DatedName(state.Global.GamePath)
			err := vid.TakeScreenshot(name)
			if err != nil {
				ntf.DisplayAndLog(ntf.Error, "Menu", err.Error())
			} else {
				ntf.DisplayAndLog(ntf.Success, "Menu", "Took a screenshot.")
			}
		},
	})

	list.children = append(list.children, entry{
		label: "Options",
		icon:  "subsetting",
		callbackOK: func() {
			list.segueNext()
			menu.Push(buildCoreOptions())
		},
	})

	list.segueMount()

	return &list
}

func (s *sceneQuick) Entry() *entry {
	return &s.entry
}

func (s *sceneQuick) segueMount() {
	genericSegueMount(&s.entry)
}

func (s *sceneQuick) segueNext() {
	genericSegueNext(&s.entry)
}

func (s *sceneQuick) segueBack() {
	genericAnimate(&s.entry)
}

func (s *sceneQuick) update(dt float32) {
	genericInput(&s.entry, dt)
}

func (s *sceneQuick) render() {
	genericRender(&s.entry)
}

func (s *sceneQuick) drawHintBar() {
	genericDrawHintBar()
}
