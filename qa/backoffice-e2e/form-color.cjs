const assert = require('node:assert/strict');

function luminance(color) {
  const channels = color.match(/[\d.]+/g)?.slice(0, 3).map(Number);
  assert.equal(channels?.length, 3, `expected RGB color, got ${color}`);
  const linear = channels.map(channel => {
    const value = channel / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722;
}

function contrast(first, second) {
  const values = [luminance(first), luminance(second)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
}

async function assertPlaceholderDistinct(input, label) {
  const colors = await input.evaluate(element => {
    const control = getComputedStyle(element);
    const placeholder = getComputedStyle(element, '::placeholder');
    return { text: control.color, background: control.backgroundColor, placeholder: placeholder.color };
  });
  assert.ok(contrast(colors.text, colors.placeholder) >= 1.8,
    `${label} placeholder must be visibly dimmer than entered text: ${JSON.stringify(colors)}`);
  assert.ok(contrast(colors.placeholder, colors.background) >= 4.5,
    `${label} placeholder must remain readable: ${JSON.stringify(colors)}`);
}

module.exports = { assertPlaceholderDistinct };
