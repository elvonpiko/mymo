# A real terminal, running the real binary. The shell line that
# starts mymo stays hidden; everything after is the program.
Output screenshots/demo.mp4
Set FontFamily "Ubuntu Sans Mono"
Set FontSize 18
Set Width 1080
Set Height 700
Set Padding 28
Set Theme "Catppuccin Mocha"
Set Framerate 24
Set TypingSpeed 40ms

Hide
Type "unset NO_COLOR"
Enter
Type "export COLORTERM=truecolor"
Enter
Type "./mymo"
Enter
Sleep 500ms
Show

# the description types itself; let it finish, then use the program
Sleep 3.6s
Type "n"
Sleep 1.8s
Down
Sleep 450ms
Down
Sleep 450ms
Up
Sleep 350ms
Up
Sleep 1s
Enter
Sleep 7s
Type "a"
Sleep 2.2s
Escape
Sleep 900ms
Type "h"
Sleep 2s
