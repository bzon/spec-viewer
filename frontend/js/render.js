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

const HEX_COLOR_RE = /^#(?:[0-9a-fA-F]{3}){1,2}$/;
const HEX_COLOR_INLINE_RE = /#(?:[0-9a-fA-F]{3}){1,2}(?=[\s,;)\]}<]|$)/g;

function swatchHtml(hex) {
  return `<span class="color-swatch" style="background:${hex}"></span>${hex}`;
}

function colorSwatchPlugin(mdi) {
  // Inline code spans: `#fb923c`
  const defaultCodeRender = mdi.renderer.rules.code_inline ||
    ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options));

  mdi.renderer.rules.code_inline = (tokens, idx, options, env, self) => {
    const content = tokens[idx].content.trim();
    if (HEX_COLOR_RE.test(content)) {
      return `<code>${swatchHtml(mdi.utils.escapeHtml(content))}</code>`;
    }
    return defaultCodeRender(tokens, idx, options, env, self);
  };

  // Plain text: replace hex colors in text tokens after core parsing
  mdi.core.ruler.after('inline', 'color_swatch_text', (state) => {
    for (const blockToken of state.tokens) {
      if (blockToken.type !== 'inline' || !blockToken.children) continue;
      const newChildren = [];
      for (const token of blockToken.children) {
        if (token.type !== 'text' || !HEX_COLOR_INLINE_RE.test(token.content)) {
          newChildren.push(token);
          continue;
        }
        HEX_COLOR_INLINE_RE.lastIndex = 0;
        let lastIndex = 0;
        let match;
        while ((match = HEX_COLOR_INLINE_RE.exec(token.content)) !== null) {
          if (match.index > lastIndex) {
            const before = new state.Token('text', '', 0);
            before.content = token.content.slice(lastIndex, match.index);
            newChildren.push(before);
          }
          const swatch = new state.Token('html_inline', '', 0);
          swatch.content = swatchHtml(mdi.utils.escapeHtml(match[0]));
          newChildren.push(swatch);
          lastIndex = match.index + match[0].length;
        }
        if (lastIndex < token.content.length) {
          const after = new state.Token('text', '', 0);
          after.content = token.content.slice(lastIndex);
          newChildren.push(after);
        }
      }
      blockToken.children = newChildren;
    }
  });
}

md.use(colorSwatchPlugin);

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
