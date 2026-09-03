import { byId } from "../ui/dom.js";

function textValue(id) {
  return byId(id).value;
}

export function bindIntakeForm(context, actions) {
  const form = byId("intake-form");
  const note = byId("note");
  const submit = byId("submit-intake");
  const dropZone = form.querySelector(".drop-zone");
  let submitting = false;

  function syncSubmit() {
    submit.disabled = submitting || !note.value.trim();
  }

  async function sendFile(file) {
    if (!file) return;
    try {
      await actions.publishFile(context, file, textValue("purpose"));
    } catch (error) {
      context.reportError(error);
    }
  }

  const fileInput = byId("file-input");
  const folderInput = byId("folder-input");
  byId("choose-file").onclick = () => fileInput.click();
  byId("choose-folder").onclick = () => folderInput.click();
  fileInput.onchange = async (event) => {
    await sendFile(event.target.files[0]);
    event.target.value = "";
  };
  folderInput.onchange = async (event) => {
    const files = Array.from(event.target.files);
    if (!files.length) return;
    try {
      await actions.publishFolder(context, files, textValue("purpose"));
    } catch (error) {
      context.reportError(error);
    }
    event.target.value = "";
  };
  note.addEventListener("input", () => {
    note.setCustomValidity("");
    syncSubmit();
  });
  form.onsubmit = async (event) => {
    event.preventDefault();
    const content = note.value.trim();
    if (!content) {
      note.setCustomValidity("请输入要保存的文本内容");
      note.reportValidity();
      note.focus();
      return;
    }
    submitting = true;
    syncSubmit();
    try {
      await actions.publish(
        context, "dashboard-note.md", content,
        "utf8", textValue("purpose"),
      );
      note.value = "";
      byId("purpose").value = "";
    } catch (error) {
      context.reportError(error);
    } finally {
      submitting = false;
      syncSubmit();
    }
  };
  dropZone.ondragover = (event) => {
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
    dropZone.classList.add("dragging");
  };
  dropZone.ondragleave = () => dropZone.classList.remove("dragging");
  dropZone.ondrop = async (event) => {
    event.preventDefault();
    dropZone.classList.remove("dragging");
    await sendFile(event.dataTransfer.files[0]);
  };
  syncSubmit();
}
