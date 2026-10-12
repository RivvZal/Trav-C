const resetForm=document.querySelector('#passwordResetForm');
const resetStatus=document.querySelector('#resetStatus');
const resetToken=new URLSearchParams(window.location.search).get('token')||'';
function showResetStatus(message,error=false){resetStatus.textContent=message;resetStatus.classList.toggle('is-error',error);resetStatus.classList.toggle('is-success',!error);resetStatus.classList.remove('hidden')}
if(!resetToken){resetForm.classList.add('hidden');showResetStatus('Tautan reset tidak valid. Kembali ke halaman masuk dan minta tautan baru.',true)}
resetForm.addEventListener('submit',async event=>{
  event.preventDefault();
  const password=document.querySelector('#newPassword').value;
  const confirmPassword=document.querySelector('#confirmNewPassword').value;
  const button=document.querySelector('#confirmResetBtn');
  resetStatus.classList.add('hidden');
  if(password.length<8||password.length>128){showResetStatus('Kata sandi harus berisi 8 sampai 128 karakter.',true);return}
  if(password!==confirmPassword){showResetStatus('Kedua kata sandi belum sama.',true);return}
  button.disabled=true;
  try{
    const response=await fetch('/api/password-reset/confirm',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token:resetToken,password,confirmPassword})});
    let data={};try{data=await response.json()}catch{}
    if(!response.ok)throw new Error(data.error||'Kata sandi belum dapat diperbarui.');
    resetForm.classList.add('hidden');
    document.querySelector('#resetTitle').textContent='Selesai, akunmu aman.';
    document.querySelector('.reset-intro').classList.add('hidden');
    document.querySelector('#resetSuccess').textContent=data.message;
    document.querySelector('#resetSuccess').classList.remove('hidden');
    document.querySelector('#newPassword').value='';
    document.querySelector('#confirmNewPassword').value='';
  }catch(error){showResetStatus(error.message,true)}
  finally{button.disabled=false}
});
