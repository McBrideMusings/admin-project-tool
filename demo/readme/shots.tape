# Driven by demo/readme/shoot.sh, which exports ADMIN_SHOTS_BIN and ADMIN_SHOTS_DEMO
# and runs vhs from docs/images/.
Output shots.gif
Set Shell "bash"
Set FontFamily "FiraCode Nerd Font"
Set FontSize 16
Set Width 1400
Set Height 760
Set Theme "Catppuccin Mocha"
Set Padding 20

Hide
Type `cd "$ADMIN_SHOTS_DEMO" && export PATH="$ADMIN_SHOTS_BIN:$PATH" PS1='$ ' && clear`
Enter
Show

# The dashboard: build and test have run, dev is still streaming under the cursor.
Type "admin"
Enter
Wait+Screen@30s /Commands/
Sleep 500ms
Down
Sleep 300ms
Enter
Sleep 1500ms
Down
Enter
Sleep 500ms
Down
Enter
Sleep 2500ms
Up
Sleep 2s
Screenshot dashboard.png

# Attached to dev: the full-screen log stream.
Enter
Sleep 2500ms
Screenshot attached.png

# Quit the dashboard, killing dev.
Escape
Sleep 500ms
Type "q"
Sleep 500ms
Enter
Sleep 1s

# A plain shell run: the output scrolls above a pinned footer with the log path and URL.
Hide
Type "clear"
Enter
Show
Type "admin dev"
Enter
Sleep 9s
Screenshot framed.png
Sleep 1s
Ctrl+C
Sleep 1s
