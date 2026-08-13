const state = { csrf: '', action: '' };
const text = (value) => document.createTextNode(String(value ?? ''));
function fill(node, value) { node.replaceChildren(text(value)); }
function project(data) {
  const node = document.querySelector('#project'); node.replaceChildren();
  [['UUID', data.id], ['Alias', data.alias], ['Scope', data.scope]]
    .forEach(([key, value]) => {
    const title = document.createElement('dt'); title.textContent = key;
    const content = document.createElement('dd'); content.textContent = value;
    node.append(title, content);
    });
}
function documents(items) {
  const body = document.querySelector('#documents'); body.replaceChildren();
  items.forEach((item) => item.versions.forEach((version) => {
    const row = document.createElement('tr');
    [item.id, version.ordinal, version.state].forEach((value) => {
      const cell = document.createElement('td'); cell.textContent = value;
      row.append(cell);
    });
    body.append(row);
  }));
}
function candidates(items) {
  const list = document.querySelector('#pending-candidates');
  list.replaceChildren();
  items.forEach((item) => {
    const entry = document.createElement('li'); entry.append(text(item.id));
    [['edit', 'Edit candidate'], ['delete', 'Soft delete'],
      ['approve', 'Request approval']].forEach(([action, label]) => {
      const button = document.createElement('button'); button.type = 'button';
      button.dataset.action = action; button.dataset.documentId = item.id;
      button.title = label; button.setAttribute('aria-label', label);
      button.textContent = action === 'edit' ? '✎' :
        action === 'delete' ? '⌫' : '✓';
      entry.append(button);
    });
    list.append(entry);
  });
}
async function refresh() {
  const response = await fetch('/api/state', { cache: 'no-store' });
  const data = await response.json(); state.csrf = data.csrf_token;
  project(data.project); documents(data.snapshot_documents);
  candidates(data.project_pending_candidates.items);
  fill(document.querySelector('#snapshot'),
    data.snapshot.generation ?? data.snapshot.state);
  fill(document.querySelector('#jobs'),
    `Writer: ${data.writer_jobs.state}; intake: ${data.intake_jobs.state}`);
  fill(document.querySelector('#degraded'),
    `Retrieval: ${data.retrieval.state}; ` +
    `governance: ${data.governance.promotion}`);
}
async function create(form) {
  const data = Object.fromEntries(new FormData(form));
  const response = await fetch('/api/candidates', {
    method: 'POST', headers: { 'Content-Type': 'application/json',
      'X-CSRF-Token': state.csrf }, body: JSON.stringify(data) });
  if (!response.ok) {
    fill(document.querySelector('#degraded'), 'Candidate request rejected.');
    return;
  }
  form.reset(); await refresh();
}
async function action(form) {
  const path = `/api/candidates/${state.action}`;
  const data = Object.fromEntries(new FormData(form));
  const response = await fetch(path, { method: 'POST', headers: {
    'Content-Type': 'application/json', 'X-CSRF-Token': state.csrf },
  body: JSON.stringify(data) });
  const result = await response.json();
  fill(document.querySelector('#action-status'), response.ok ? result.status :
    `${result.status ?? result.error.code}: ${result.promotion ?? ''}`);
  if (response.ok) { await refresh(); actionDialog.close(); }
}
const dialog = document.querySelector('#candidate-dialog');
const actionDialog = document.querySelector('#action-dialog');
const actionForm = document.querySelector('#action-form');
document.querySelector('#refresh').addEventListener('click', refresh);
document.querySelector('#new-candidate').addEventListener('click',
  () => dialog.showModal());
dialog.addEventListener('click', (event) => {
  if (event.target === dialog) dialog.close();
});
actionDialog.addEventListener('click', (event) => {
  if (event.target === actionDialog) actionDialog.close();
});
document.querySelector('#pending-candidates').addEventListener('click',
  (event) => {
  const button = event.target.closest('button[data-action]');
  if (!button) return;
  state.action = button.dataset.action;
  actionForm.document_id.value = button.dataset.documentId;
  fill(document.querySelector('#action-title'), button.title);
  document.querySelector('#edit-content').hidden = state.action !== 'edit';
  document.querySelector('#edit-source').hidden = state.action !== 'edit';
  fill(document.querySelector('#action-status'), ''); actionDialog.showModal();
});
document.querySelector('#candidate-form').addEventListener('submit',
  (event) => {
  if (event.submitter.value !== 'cancel') {
    event.preventDefault();
    create(event.currentTarget).then(() => dialog.close());
  }
});
actionForm.addEventListener('submit', (event) => {
  if (event.submitter.value !== 'cancel') {
    event.preventDefault(); action(event.currentTarget);
  }
});
refresh();
