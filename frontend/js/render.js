import markdownit from 'https://esm.sh/markdown-it@14?bundle';
import { createHighlighter } from 'https://esm.sh/shiki@3?bundle';

let highlighter = null;
let currentTheme = 'github-dark';

const themeMap = {
  'github-dark': 'github-dark',
  'github-light': 'github-light',
  'dracula': 'dracula',
  'nord': 'nord',
  'solarized': 'solarized-dark',
};

const md = markdownit({
  html: false,
  linkify: true,
  typographer: true,
  highlight: (code, lang) => {
    if (!highlighter || !lang) return '';
    try {
      return highlighter.codeToHtml(code, {
        lang,
        theme: themeMap[currentTheme] || 'github-dark',
      });
    } catch {
      return '';
    }
  },
});

export async function initRenderer() {
  highlighter = await createHighlighter({
    themes: Object.values(themeMap),
    langs: [
      'javascript', 'typescript', 'go', 'python', 'bash', 'shell',
      'json', 'yaml', 'toml', 'html', 'css', 'sql', 'markdown',
      'rust', 'java', 'ruby', 'php', 'swift', 'kotlin', 'c', 'cpp',
      'diff', 'dockerfile', 'graphql', 'jsx', 'tsx',
    ],
  });
}

export function render(markdownText) {
  return md.render(markdownText);
}

export function setTheme(themeName) {
  currentTheme = themeName;
}

export function getTheme() {
  return currentTheme;
}
