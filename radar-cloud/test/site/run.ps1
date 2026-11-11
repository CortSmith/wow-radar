# Runs the frontend tests headless with VS Code's bundled node.
$code = "C:\Users\corts\AppData\Local\Programs\Microsoft VS Code\Code.exe"
$env:ELECTRON_RUN_AS_NODE = "1"
$dir = Split-Path -Parent $MyInvocation.MyCommand.Path
$files = Get-ChildItem $dir -Filter *.test.js | ForEach-Object { $_.FullName }
& $code --test @files | Out-Host
exit $LASTEXITCODE
