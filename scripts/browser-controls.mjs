// Interact with the same DOM options used by mouse, touch, and keyboard users.
export async function chooseOption(trigger, choice) {
  await trigger.click()
  const list = trigger.page().getByRole('listbox')
  const option = typeof choice === 'object' && choice.label !== undefined
    ? list.getByRole('option', { name: choice.label, exact: true })
    : list.getByRole('option').locator(`xpath=self::*[@data-value=${xpathLiteral(typeof choice === 'object' ? choice.value : choice)}]`)
  await option.click()
}
function xpathLiteral(value) {
  if (!value.includes("'")) return `'${value}'`
  return `concat('${value.split("'").join("',\"'\",'")}')`
}
export async function acceptConfirmation(page, name) {
  const dialog = page.getByRole('alertdialog')
  await (name ? dialog.getByRole('button', { name, exact: true }) : dialog.locator('.button-danger, .button-primary')).click()
}
