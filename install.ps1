$link = "https://github.com/Ernest1101/Sudotl/releases/latest/download/SudotlCli.exe"

$outfile = "$env:TEMP\SudotlCli.exe"

Write-Output "Downloading installer to $outfile"

Invoke-WebRequest -Uri "$link" -OutFile "$outfile"

Write-Output ""

Start-Process -Wait -NoNewWindow -FilePath "$outfile"

# Cleanup
Remove-Item -Force "$outfile"
