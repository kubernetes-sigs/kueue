/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Taint value, toleration key, value, operator and effect are all optional,
// and the API omits them when empty. Both helpers follow kubectl describe.

// formatTaint returns key=value:Effect, or key:Effect when there is no value.
export const formatTaint = (taint) => {
  let text = taint?.key || '';
  if (taint?.value) text += `=${taint.value}`;
  if (taint?.effect) text += `:${taint.effect}`;
  return text;
};

// formatToleration returns key=value:Effect, with " op=Exists" for the
// Exists operator and " for Ns" when tolerationSeconds is set.
export const formatToleration = (toleration) => {
  const parts = [];
  let text = toleration?.key || '';
  if (toleration?.value) text += `=${toleration.value}`;
  if (toleration?.effect) text += `:${toleration.effect}`;
  if (text) parts.push(text);
  if (toleration?.operator === 'Exists') parts.push('op=Exists');
  if (toleration?.tolerationSeconds != null) parts.push(`for ${toleration.tolerationSeconds}s`);
  return parts.join(' ');
};
