#!/usr/bin/env node

const { execFileSync } = require('child_process');

function which(cmd) {
  try {
    execFileSync('which', [cmd], { stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
}

if (which('spec-viewer')) {
  console.log('spec-viewer: already installed');
  process.exit(0);
}

if (which('go')) {
  console.log('spec-viewer: installing via go install...');
  try {
    execFileSync('go', ['install', 'github.com/bzon/spec-viewer/cmd/spec-viewer@latest'], { stdio: 'inherit' });
    console.log('spec-viewer: installed successfully');
  } catch (err) {
    console.error('spec-viewer: go install failed. Install manually:');
    console.error('  go install github.com/bzon/spec-viewer/cmd/spec-viewer@latest');
    console.error('  or download from https://github.com/bzon/spec-viewer/releases');
  }
} else {
  console.log('spec-viewer: Go not found. Install the binary manually:');
  console.log('  Option 1: Install Go, then run: go install github.com/bzon/spec-viewer/cmd/spec-viewer@latest');
  console.log('  Option 2: Download from https://github.com/bzon/spec-viewer/releases');
}
