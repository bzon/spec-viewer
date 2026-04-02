let observer = null;
let activeId = null;

export function buildTOC(container, contentEl) {
  const headings = contentEl.querySelectorAll('h2, h3');
  container.textContent = '';
  const items = [];

  headings.forEach((heading) => {
    const id = heading.id || slugify(heading.textContent);
    heading.id = id;

    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'toc-item level-' + heading.tagName.toLowerCase().charAt(1);
    item.textContent = heading.textContent;
    item.dataset.target = id;

    item.addEventListener('click', () => {
      heading.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });

    container.appendChild(item);
    items.push({ el: item, headingEl: heading, id });
  });

  setupScrollTracking(items, contentEl);
  return items;
}

function setupScrollTracking(items, contentEl) {
  if (observer) observer.disconnect();

  // root must be the scroll container, not the rendered content div
  const scrollContainer = contentEl.closest('.content') || contentEl.parentElement;

  observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) {
          setActive(entry.target.id, items);
          break;
        }
      }
    },
    { root: scrollContainer, rootMargin: '-10% 0px -80% 0px', threshold: 0 }
  );

  items.forEach(({ headingEl }) => observer.observe(headingEl));
}

function setActive(id, items) {
  if (id === activeId) return;
  activeId = id;
  items.forEach(({ el, id: itemId }) => {
    if (itemId === id) el.classList.add('active');
    else el.classList.remove('active');
  });
}

function slugify(text) {
  return text.toLowerCase().replace(/[^\w\s-]/g, '').replace(/\s+/g, '-').replace(/-+/g, '-').trim();
}

export function destroyTOC() {
  if (observer) { observer.disconnect(); observer = null; }
  activeId = null;
}
