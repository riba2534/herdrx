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

export async function openSiteNav(page, name) {
  const more = page.getByRole('button', { name: '更多', exact: true })
  for (let attempt = 0; attempt < 3; attempt++) {
    try {
      if (await more.isVisible().catch(() => false)) {
        await more.click()
        await page.getByRole('menuitem', { name, exact: true }).click({ timeout: 4000 })
        return
      }
      await page.getByRole('button', { name, exact: true }).click()
      return
    } catch (error) {
      if (attempt === 2) throw error
      await page.keyboard.press('Escape').catch(() => {})
    }
  }
}
