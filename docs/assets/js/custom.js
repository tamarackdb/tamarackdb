// Highlight the top menu entry for the section the current page is in.
// Doks only highlights an entry whose name matches the page title, so
// "Guides" would stay plain on the Deployment or Integration page.
// Each entry links to a page under /docs/<section>/, which gives the
// section to match against.
document.querySelectorAll('.navbar-nav .nav-link').forEach((link) => {
  const parts = new URL(link.href).pathname.split('/');
  if (parts[1] !== 'docs' || !parts[2]) return;
  const section = `/docs/${parts[2]}/`;
  if (window.location.pathname.startsWith(section)) {
    link.classList.add('active');
    link.setAttribute('aria-current', 'true');
  }
});
