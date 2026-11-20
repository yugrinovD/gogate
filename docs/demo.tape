Output docs/demo.gif
Set FontSize 15
Set Width 1400
Set Height 900
Set Padding 24
Set Shell "bash"
Set TypingSpeed 35ms

Type "export GOGATE_DEMO_URL=$(cat build/demo-url.txt)"
Enter
Type "export GOGATE_DEMO_KEY=$(cat build/demo-key.txt)"
Enter
Type "export GOGATE_DEMO_RL_KEY=$(cat build/demo-rate-limit-key.txt)"
Enter
Type "python3 scripts/demo-client.py"
Enter
Sleep 25s
