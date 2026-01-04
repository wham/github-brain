# GitHub Brain Demo
# This tape demonstrates starting the app and running the Pull operation.
#
# Prerequisites:
# - VHS installed (brew install vhs)
# - github-brain built (go build -o github-brain .)
# - Valid GitHub token in ~/.github-brain/.env or test environment
#
# Usage:
#   vhs tapes/demo.tape
#
# For development with test data, use:
#   GITHUB_BRAIN_HOME=./test-home vhs tapes/demo.tape

Output tapes/demo.gif

# Terminal settings
Set Shell bash
Set FontFamily "JetBrains Mono"
Set FontSize 14
Set Width 800
Set Height 600
Set Theme "Catppuccin Mocha"
Set Padding 20
Set Framerate 30
Set PlaybackSpeed 1

# Start the application
Type "github-brain"
Sleep 500ms
Enter
Sleep 2s

# The main menu is displayed with Pull selected by default
# Press Enter to start Pull
Enter
Sleep 8s

# Show the pull operation in progress
# Wait for activity to display
Sleep 5s

# Exit the application
Ctrl+C
Sleep 500ms
