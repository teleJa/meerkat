Unicode true
Name "Meerkat updater test fixture"
OutFile "Meerkat-Setup-Fails.exe"
RequestExecutionLevel user
SilentInstall silent

Section
  SetOutPath "$INSTDIR"
  FileOpen $0 "$INSTDIR\Meerkat.exe" w
  IfErrors fixture_error
  FileWrite $0 "partial install"
  FileClose $0
  SetErrorLevel 2
  Quit
  fixture_error:
  SetErrorLevel 3
SectionEnd
