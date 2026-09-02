@echo off
pwsh -NoProfile -ExecutionPolicy Bypass -Command "& '%~dp0afterburn.ps1' @args" %*
