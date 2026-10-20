// Exercise the same visible confirmation interaction as an operator.
exports.submit = async function submit(page, button) {
  await button.click();
  const dialog = page.getByRole('dialog');
  if (await dialog.isVisible()) await dialog.locator('[data-confirm-accept]').click();
};
exports.cancelConfirmation = async function cancelConfirmation(page, button) {
  await button.click();
  const dialog = page.getByRole('dialog');
  await dialog.waitFor({ state: 'visible' });
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click();
  await dialog.waitFor({ state: 'hidden' });
};
