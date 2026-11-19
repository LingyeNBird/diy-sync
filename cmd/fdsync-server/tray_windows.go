//go:build windows

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"fdsync/internal/server"
)

// trayText is the tray menu in the Windows display language (English or Chinese).
type trayText struct {
	open, pauseAll, resume, untilResumed, autostart, quit string
	running, paused, pausedUntil, conflicts               string
	minutes, hours                                        string
}

var trayEN = trayText{
	open: "Open panel", pauseAll: "Pause all", resume: "Resume now", untilResumed: "Until I resume",
	autostart: "Start with Windows", quit: "Quit fdsync",
	running: "Running · %d of %d connections online", paused: "Paused", pausedUntil: "Paused until %s",
	conflicts: "%d conflicts", minutes: "For %d minutes", hours: "For %d hours",
}

var trayZH = trayText{
	open: "打开面板", pauseAll: "全部暂停", resume: "立即恢复", untilResumed: "直到我手动恢复",
	autostart: "开机时启动", quit: "退出 fdsync",
	running: "运行中 · %d/%d 个连接在线", paused: "已暂停", pausedUntil: "已暂停至 %s",
	conflicts: "%d 个冲突", minutes: "%d 分钟", hours: "%d 小时",
}

func trayLang() trayText {
	langs, _ := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if len(langs) > 0 && strings.HasPrefix(strings.ToLower(langs[0]), "zh") {
		return trayZH
	}
	return trayEN
}

// runUI shows the tray icon until the server stops (done) or the user
// quits (which calls quit and then waits for done). noTray runs headless.
func runUI(_ context.Context, quit func(), srv *server.Server, panelURL string, auto *runKey, done <-chan struct{}, noTray bool) {
	if noTray {
		<-done
		return
	}
	tx := trayLang()
	systray.Run(func() {
		systray.SetIcon(trayIcon(iconRunning, false))
		systray.SetTooltip("fdsync")
		status := systray.AddMenuItem("fdsync", "")
		status.Disable()
		systray.AddSeparator()
		open := systray.AddMenuItem(tx.open, "")
		pause := systray.AddMenuItem(tx.pauseAll, "")
		durations := []int{10, 30, 60, 120}
		var pauseFor []*systray.MenuItem
		for _, m := range durations {
			label := fmt.Sprintf(tx.minutes, m)
			if m >= 60 {
				label = fmt.Sprintf(tx.hours, m/60)
			}
			pauseFor = append(pauseFor, pause.AddSubMenuItem(label, ""))
		}
		pauseForever := pause.AddSubMenuItem(tx.untilResumed, "")
		resume := systray.AddMenuItem(tx.resume, "")
		resume.Hide()
		systray.AddSeparator()
		autostart := systray.AddMenuItemCheckbox(tx.autostart, "", false)
		if on, err := auto.Enabled(); err == nil && on {
			autostart.Check()
		}
		systray.AddSeparator()
		quitItem := systray.AddMenuItem(tx.quit, "")

		update := func() {
			st := srv.Status()
			kind := iconRunning
			line := fmt.Sprintf(tx.running, st.Online, st.Connections)
			if st.Paused {
				kind = iconPaused
				line = tx.paused
				if !st.PausedUntil.IsZero() {
					line = fmt.Sprintf(tx.pausedUntil, st.PausedUntil.Local().Format("15:04"))
				}
				resume.Show()
			} else {
				resume.Hide()
			}
			if st.Conflicts > 0 {
				line += " · " + fmt.Sprintf(tx.conflicts, st.Conflicts)
			}
			status.SetTitle(line)
			systray.SetTooltip("fdsync — " + line)
			systray.SetIcon(trayIcon(kind, st.Conflicts > 0))
		}
		update()

		go func() {
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			last := ""
			for {
				select {
				case <-done:
					systray.Quit()
					return
				case <-tick.C:
					// Only touch the icon when something changed.
					st := srv.Status()
					if k := fmt.Sprint(st); k != last {
						last = k
						update()
					}
				case <-open.ClickedCh:
					openBrowser(panelURL)
				case <-pauseForever.ClickedCh:
					srv.SetPauseAll(true, time.Time{}, false)
					update()
				case <-resume.ClickedCh:
					srv.SetPauseAll(false, time.Time{}, false)
					update()
				case <-autostart.ClickedCh:
					want := !autostart.Checked()
					if err := auto.Set(want); err != nil {
						showError("fdsync", "Could not change “Start with Windows”: "+err.Error())
					} else if want {
						autostart.Check()
					} else {
						autostart.Uncheck()
					}
				case <-quitItem.ClickedCh:
					quit()
				}
			}
		}()
		for i, m := range durations {
			go func() {
				for range pauseFor[i].ClickedCh {
					srv.SetPauseAll(true, time.Now().Add(time.Duration(m)*time.Minute), false)
					update()
				}
			}()
		}
	}, func() {})
	<-done
}
